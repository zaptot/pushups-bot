"""Discord-бот для учёта отжиманий: споры, матчи Dota 2, гражданство, суд, законы и награды.

Данные — в Postgres (Supabase).
"""

import asyncio
import collections
import datetime as dt
import math
import os
import random
import re
from zoneinfo import ZoneInfo

import aiohttp
import asyncpg
import discord
from discord import app_commands
from discord.ext import tasks
from dotenv import load_dotenv

load_dotenv()
TOKEN = os.getenv("DISCORD_TOKEN")
GUILD_ID = os.getenv("GUILD_ID")  # необязательно: команды появятся на сервере мгновенно
DATABASE_URL = os.getenv("DATABASE_URL")
TZ = ZoneInfo(os.getenv("TIMEZONE", "Europe/Berlin"))  # для наград «1-го числа»
WINDOW_DAYS = 365  # период для лидербордов и остатков
TOP_SIZE = 15
DONE_MIN = 10  # меньше за раз записать через /done нельзя

# --- Dota 2 ---
DOTA_CHANNEL_ID = os.getenv("DOTA_CHANNEL_ID")  # куда писать результаты автопроверки
DOTA_CHECK_MINUTES = int(os.getenv("DOTA_CHECK_MINUTES", "30"))
OPENDOTA_API_KEY = os.getenv("OPENDOTA_API_KEY")  # необязательно, без ключа ~2000 запросов в день
MIN_MATCH_SECONDS = 10 * 60  # более короткие матчи (ремейки, ливы) не считаются
PARTY_MIN_LINKED = 2  # матч считается, только если в нём минимум столько привязанных игроков
SOLO_DELAY = dt.timedelta(hours=2)  # соло-матч засчитывается не раньше, чем через столько после его конца
OPENDOTA = "https://api.opendota.com/api"
STEAM64_BASE = 76561197960265728

# --- PUBG ---
PUBG_API_KEY = os.getenv("PUBG_API_KEY")  # developer.pubg.com
PUBG_SHARD = os.getenv("PUBG_SHARD", "steam")
PUBG_API = f"https://api.pubg.com/shards/{PUBG_SHARD}"
PUBG_MATCH_TYPES = {"official", "competitive"}  # обычные и рейтинговые; аркады, кастомки и тренировки — нет
PUBG_SAFE_POINTS = 300  # набрал столько очков — без отжиманий
PUBG_PUSHUPS = 10

# --- государство ---
CIVIC_CHANNEL_ID = os.getenv("CIVIC_CHANNEL_ID") or DOTA_CHANNEL_ID  # куда писать награды
CITIZEN_MAX_DEBT = 100  # чтобы стать гражданином, долг должен быть меньше
FREEZE_DEBT = 300  # при долге больше — привилегии гражданина заморожены
VOUCHES_NEEDED = 2
CITIZEN_MIN_DAYS = 14  # ручаться можно только за тех, кто на сервере не меньше стольких дней
PRESIDENT_TERM_DAYS = 30  # минимальный срок президента до перевыборов
AWARD_MIN_GAMES = 10  # для ролей и наград нужно больше стольких пати-игр за период
LAW_HOURS = 24
ELECTION_PHASE_HOURS = 24  # и выдвижение, и голосование
ROLE_NAMES = {
    "citizen": "Гражданин",
    "president": "Президент",
    "ruiner": "Топ руинер",
    "decent": "Порядочный гражданин",
    "marginal": "Маргинал",
}
CONSTITUTION = [
    "Каждый гражданин обязан делать отжимания.",
    f"Если у человека накопилось более {FREEZE_DEBT} отжиманий, все привилегии гражданства теряются "
    "(замораживаются, пока долг не станет меньше).",
    "Президента нельзя забанить.",
]
MONTHS = ["январь", "февраль", "март", "апрель", "май", "июнь",
          "июль", "август", "сентябрь", "октябрь", "ноябрь", "декабрь"]

SCHEMA = """
CREATE TABLE IF NOT EXISTS entries (
    id         BIGSERIAL PRIMARY KEY,
    guild_id   BIGINT      NOT NULL,
    user_id    BIGINT      NOT NULL,              -- чьи это отжимания
    kind       TEXT        NOT NULL CHECK (kind IN ('owe', 'done')),
    amount     INTEGER     NOT NULL CHECK (amount > 0),
    author_id  BIGINT      NOT NULL,              -- кто создал запись
    reason     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_entries_guild_time ON entries (guild_id, created_at);
-- откуда взялись отжимания: manual (/owe, /done), dota, court
ALTER TABLE entries ADD COLUMN IF NOT EXISTS source TEXT NOT NULL DEFAULT 'manual';
UPDATE entries SET source = 'dota' WHERE source = 'manual' AND reason LIKE 'Dota %, матч %';
-- /done_ludoman может засчитать 0
ALTER TABLE entries DROP CONSTRAINT IF EXISTS entries_amount_check;
ALTER TABLE entries ADD CONSTRAINT entries_amount_check CHECK (amount >= 0);

CREATE TABLE IF NOT EXISTS dota_links (
    guild_id   BIGINT      NOT NULL,
    user_id    BIGINT      NOT NULL,
    account_id BIGINT      NOT NULL,              -- 32-битный Dota/Steam account id
    linked_by  BIGINT      NOT NULL,
    linked_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id)
);

CREATE TABLE IF NOT EXISTS dota_matches (          -- уже обработанные пати-матчи
    guild_id     BIGINT      NOT NULL,
    user_id      BIGINT      NOT NULL,
    match_id     BIGINT      NOT NULL,
    kills        INTEGER     NOT NULL,
    deaths       INTEGER     NOT NULL,
    assists      INTEGER     NOT NULL,
    pushups      INTEGER     NOT NULL,
    played_at    TIMESTAMPTZ NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id, match_id)
);

-- соло-игры в Dota: NULL — выключены, иначе момент включения
ALTER TABLE dota_links ADD COLUMN IF NOT EXISTS solo_since TIMESTAMPTZ;
-- false — матч засчитан как соло (для отчётов; в наградах учитывается как обычный)
ALTER TABLE dota_matches ADD COLUMN IF NOT EXISTS is_party BOOLEAN NOT NULL DEFAULT true;

CREATE TABLE IF NOT EXISTS pubg_links (
    guild_id   BIGINT      NOT NULL,
    user_id    BIGINT      NOT NULL,
    account_id TEXT        NOT NULL,              -- account.xxxxxxxx
    name       TEXT        NOT NULL,
    linked_by  BIGINT      NOT NULL,
    linked_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id)
);

CREATE TABLE IF NOT EXISTS pubg_matches (          -- засчитанные пати-матчи
    guild_id     BIGINT      NOT NULL,
    user_id      BIGINT      NOT NULL,
    match_id     TEXT        NOT NULL,
    kills        INTEGER     NOT NULL,
    assists      INTEGER     NOT NULL,
    damage       INTEGER     NOT NULL,
    points       INTEGER     NOT NULL,
    pushups      INTEGER     NOT NULL,
    played_at    TIMESTAMPTZ NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id, match_id)
);

CREATE TABLE IF NOT EXISTS pubg_checked (          -- уже разобранные матчи (засчитанные и нет)
    guild_id   BIGINT      NOT NULL,
    match_id   TEXT        NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, match_id)
);

CREATE TABLE IF NOT EXISTS citizens (
    guild_id  BIGINT      NOT NULL,
    user_id   BIGINT      NOT NULL,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id)
);

CREATE TABLE IF NOT EXISTS vouches (
    guild_id     BIGINT      NOT NULL,
    candidate_id BIGINT      NOT NULL,
    voucher_id   BIGINT      NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, candidate_id, voucher_id)
);

CREATE TABLE IF NOT EXISTS polls (                 -- все голосования: суд, законы, выборы и т.д.
    id         BIGSERIAL PRIMARY KEY,
    guild_id   BIGINT      NOT NULL,
    kind       TEXT        NOT NULL,              -- revoke | accuse | law | marginal | election
    status     TEXT        NOT NULL,              -- nominating | open | passed | failed
    creator_id BIGINT      NOT NULL,
    target_id  BIGINT,
    lawyer_id  BIGINT,
    amount     INTEGER,
    text       TEXT,
    channel_id BIGINT,
    message_id BIGINT,
    result     TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    ends_at    TIMESTAMPTZ NOT NULL
);

CREATE TABLE IF NOT EXISTS poll_votes (
    poll_id  BIGINT NOT NULL REFERENCES polls (id) ON DELETE CASCADE,
    voter_id BIGINT NOT NULL,
    choice   TEXT   NOT NULL,                     -- yes | no | id кандидата
    PRIMARY KEY (poll_id, voter_id)
);

CREATE TABLE IF NOT EXISTS election_candidates (
    poll_id BIGINT NOT NULL REFERENCES polls (id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL,
    PRIMARY KEY (poll_id, user_id)
);

CREATE TABLE IF NOT EXISTS laws (
    id         BIGSERIAL PRIMARY KEY,
    guild_id   BIGINT      NOT NULL,
    text       TEXT        NOT NULL,
    poll_id    BIGINT,
    adopted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS presidents (
    id         BIGSERIAL PRIMARY KEY,
    guild_id   BIGINT      NOT NULL,
    user_id    BIGINT      NOT NULL,
    elected_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS marginals (
    guild_id BIGINT      NOT NULL,
    user_id  BIGINT      NOT NULL,
    since    TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id)
);

CREATE TABLE IF NOT EXISTS role_holders (          -- кому бот выдал роли (чтобы потом снять)
    guild_id BIGINT NOT NULL,
    role_key TEXT   NOT NULL,
    user_id  BIGINT NOT NULL,
    PRIMARY KEY (guild_id, role_key, user_id)
);

CREATE TABLE IF NOT EXISTS award_runs (
    guild_id BIGINT      NOT NULL,
    period   TEXT        NOT NULL,                -- месяц итогов, например 2026-11
    run_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, period)
);
"""

# ---------- бот ----------


class PushupBot(discord.Client):
    def __init__(self) -> None:
        super().__init__(intents=discord.Intents.default())
        self.tree = app_commands.CommandTree(self)
        self.pool: asyncpg.Pool | None = None
        self.http_session: aiohttp.ClientSession | None = None
        self.dota_lock = asyncio.Lock()  # чтобы ручной и автоматический подсчёт не шли одновременно
        self.pubg_lock = asyncio.Lock()

    async def setup_hook(self) -> None:
        # statement_cache_size=0 — чтобы работать и через пулер Supabase (pgbouncer)
        self.pool = await asyncpg.create_pool(DATABASE_URL, min_size=1, max_size=5, statement_cache_size=0)
        async with self.pool.acquire() as conn:
            await conn.execute(SCHEMA)
        self.http_session = aiohttp.ClientSession(timeout=aiohttp.ClientTimeout(total=20))
        dota_loop.start()
        pubg_loop.start()
        polls_loop.start()
        civic_loop.start()

        if GUILD_ID:
            guild = discord.Object(id=int(GUILD_ID))
            self.tree.copy_global_to(guild=guild)
            await self.tree.sync(guild=guild)
        else:
            await self.tree.sync()  # глобальная синхронизация может занять до часа

    async def close(self) -> None:
        dota_loop.cancel()
        pubg_loop.cancel()
        polls_loop.cancel()
        civic_loop.cancel()
        if self.http_session:
            await self.http_session.close()
        if self.pool:
            await self.pool.close()
        await super().close()

    async def on_ready(self) -> None:
        print(f"Запущен как {self.user} (id {self.user.id})")

    async def on_interaction(self, interaction: discord.Interaction) -> None:
        # кнопки и меню голосований (работают и после перезапуска бота)
        if interaction.type != discord.InteractionType.component:
            return
        parts = (interaction.data or {}).get("custom_id", "").split(":")
        if parts[0] == "vote" and len(parts) == 3:
            await handle_vote(interaction, int(parts[1]), parts[2])
        elif parts[0] == "elect" and len(parts) == 2:
            values = (interaction.data or {}).get("values") or []
            if values:
                await handle_vote(interaction, int(parts[1]), values[0])


bot = PushupBot()

# ---------- работа с базой ----------


async def add_entry(
    guild_id: int, user_id: int, kind: str, amount: int, author_id: int, reason: str | None, source: str = "manual"
) -> None:
    await bot.pool.execute(
        "INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason, source) "
        "VALUES ($1, $2, $3, $4, $5, $6, $7)",
        guild_id, user_id, kind, amount, author_id, reason, source,
    )


async def guild_stats(guild_id: int) -> dict[int, tuple[int, int]]:
    """user_id -> (назначено, сделано) за последние WINDOW_DAYS дней."""
    rows = await bot.pool.fetch(
        "SELECT user_id, "
        "  COALESCE(SUM(amount) FILTER (WHERE kind = 'owe'), 0)  AS owed, "
        "  COALESCE(SUM(amount) FILTER (WHERE kind = 'done'), 0) AS done "
        "FROM entries WHERE guild_id = $1 AND created_at >= now() - make_interval(days => $2) "
        "GROUP BY user_id",
        guild_id, WINDOW_DAYS,
    )
    return {r["user_id"]: (r["owed"], r["done"]) for r in rows}


async def ludoman_balance(guild_id: int) -> dict[int, int]:
    """user_id -> сколько отжиманий выиграно (+) или проиграно (−) на /done_ludoman за WINDOW_DAYS дней."""
    rows = await bot.pool.fetch(
        "SELECT user_id, SUM(amount - substring(reason from 'лудомания: ([0-9]+) ×')::int) AS delta "
        "FROM entries WHERE guild_id = $1 AND kind = 'done' AND reason LIKE '🎰 лудомания:%' "
        "AND created_at >= now() - make_interval(days => $2) GROUP BY user_id",
        guild_id, WINDOW_DAYS,
    )
    return {r["user_id"]: r["delta"] for r in rows}


async def user_stats(guild_id: int, user_id: int) -> tuple[int, int, int]:
    owed, done = (await guild_stats(guild_id)).get(user_id, (0, 0))
    return owed, done, max(0, owed - done)


def now_utc() -> dt.datetime:
    return dt.datetime.now(dt.timezone.utc)


def no_guild_reply() -> str:
    return "Эта команда работает только на сервере."


# ---------- гражданство: проверки ----------


async def citizen_ids(guild_id: int) -> list[int]:
    rows = await bot.pool.fetch("SELECT user_id FROM citizens WHERE guild_id = $1 ORDER BY joined_at", guild_id)
    return [r["user_id"] for r in rows]


async def is_citizen(guild_id: int, user_id: int) -> bool:
    return bool(await bot.pool.fetchval(
        "SELECT 1 FROM citizens WHERE guild_id = $1 AND user_id = $2", guild_id, user_id
    ))


async def active_citizens(guild_id: int) -> list[int]:
    """Граждане без заморозки (долг не больше FREEZE_DEBT)."""
    stats = await guild_stats(guild_id)
    return [
        uid for uid in await citizen_ids(guild_id)
        if max(0, stats.get(uid, (0, 0))[0] - stats.get(uid, (0, 0))[1]) <= FREEZE_DEBT
    ]


async def current_president(guild_id: int) -> asyncpg.Record | None:
    return await bot.pool.fetchrow(
        "SELECT user_id, elected_at FROM presidents WHERE guild_id = $1 ORDER BY elected_at DESC LIMIT 1", guild_id
    )


async def is_president(guild_id: int, user_id: int) -> bool:
    president = await current_president(guild_id)
    return president is not None and president["user_id"] == user_id


async def require_active_citizen(interaction: discord.Interaction) -> bool:
    """Отвечает отказом и возвращает False, если пользователь не гражданин или заморожен."""
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return False
    if not await is_citizen(interaction.guild_id, interaction.user.id):
        await interaction.response.send_message("Это могут только граждане.", ephemeral=True)
        return False
    _, _, remaining = await user_stats(interaction.guild_id, interaction.user.id)
    if remaining > FREEZE_DEBT:
        await interaction.response.send_message(
            f"❄️ Твои привилегии заморожены: долг **{remaining}** > {FREEZE_DEBT}. Отожмись!", ephemeral=True
        )
        return False
    return True


# ---------- роли ----------


async def get_role(guild: discord.Guild, key: str) -> discord.Role | None:
    role = discord.utils.get(guild.roles, name=ROLE_NAMES[key])
    if role is None:
        try:
            role = await guild.create_role(name=ROLE_NAMES[key], reason="Роль бота отжиманий")
        except discord.HTTPException as e:
            print(f"Не удалось создать роль {ROLE_NAMES[key]}: {e}")
            return None
    return role


async def change_role(guild: discord.Guild, user_id: int, role: discord.Role, add: bool) -> bool:
    member = guild.get_member(user_id)
    if member is None:
        try:
            member = await guild.fetch_member(user_id)
        except discord.NotFound:
            return not add  # участник ушёл с сервера — снимать нечего
        except discord.HTTPException:
            return False
    try:
        if add:
            await member.add_roles(role, reason="Бот отжиманий")
        else:
            await member.remove_roles(role, reason="Бот отжиманий")
        return True
    except discord.HTTPException as e:
        print(f"Не удалось {'выдать' if add else 'снять'} роль {role.name} у {user_id}: {e}")
        return False


async def set_role_holders(guild: discord.Guild, key: str, user_ids: set[int]) -> None:
    """Делает так, чтобы роль была ровно у user_ids (среди тех, кому её выдавал бот)."""
    role = await get_role(guild, key)
    if role is None:
        return
    rows = await bot.pool.fetch(
        "SELECT user_id FROM role_holders WHERE guild_id = $1 AND role_key = $2", guild.id, key
    )
    old = {r["user_id"] for r in rows}
    held = set(old)
    for uid in old - user_ids:
        if await change_role(guild, uid, role, add=False):
            held.discard(uid)
    for uid in user_ids - old:
        if await change_role(guild, uid, role, add=True):
            held.add(uid)
    async with bot.pool.acquire() as conn, conn.transaction():
        await conn.execute("DELETE FROM role_holders WHERE guild_id = $1 AND role_key = $2", guild.id, key)
        await conn.executemany(
            "INSERT INTO role_holders (guild_id, role_key, user_id) VALUES ($1, $2, $3)",
            [(guild.id, key, uid) for uid in held],
        )


async def sync_roles(guild: discord.Guild) -> None:
    """Гражданин, Президент, Маргинал — по данным в базе."""
    await set_role_holders(guild, "citizen", set(await citizen_ids(guild.id)))
    president = await current_president(guild.id)
    await set_role_holders(guild, "president", {president["user_id"]} if president else set())
    rows = await bot.pool.fetch("SELECT user_id FROM marginals WHERE guild_id = $1", guild.id)
    await set_role_holders(guild, "marginal", {r["user_id"] for r in rows})


async def member_name(guild: discord.Guild, user_id: int) -> str:
    member = guild.get_member(user_id)
    if member is None:
        try:
            member = await guild.fetch_member(user_id)
        except discord.HTTPException:
            return str(user_id)
    return member.display_name


# ---------- команды: отжимания ----------


@bot.tree.command(name="owe", description="Назначить участнику отжимания (проигранный спор)")
@app_commands.describe(user="Кто должен отжиматься", amount="Сколько отжиманий", reason="За что, например «3 кила × 10»")
async def owe(
    interaction: discord.Interaction,
    user: discord.Member,
    amount: app_commands.Range[int, 1, 10000],
    reason: str | None = None,
) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    if user.bot:
        await interaction.response.send_message("Боты не отжимаются 🤖", ephemeral=True)
        return
    await add_entry(interaction.guild_id, user.id, "owe", amount, interaction.user.id, reason)
    _, _, remaining = await user_stats(interaction.guild_id, user.id)
    text = f"💪 {interaction.user.mention} назначил {user.mention} **{amount}** отжиманий"
    if reason:
        text += f" — {reason}"
    text += f"\nТеперь осталось: **{remaining}**"
    await interaction.response.send_message(text)


@bot.tree.command(name="done", description="Записать сделанные отжимания")
@app_commands.describe(amount=f"Сколько отжиманий сделал (минимум {DONE_MIN})")
async def done(interaction: discord.Interaction, amount: app_commands.Range[int, DONE_MIN, 10000]) -> None:
    await record_done(interaction, amount, ludoman=False)


@bot.tree.command(name="done_ludoman", description="Записать отжимания × случайный коэффициент от 0 до 2 🎰")
@app_commands.describe(amount=f"Сколько отжиманий сделал (минимум {DONE_MIN})")
async def done_ludoman(interaction: discord.Interaction, amount: app_commands.Range[int, DONE_MIN, 10000]) -> None:
    await record_done(interaction, amount, ludoman=True)


async def record_done(interaction: discord.Interaction, amount: int, ludoman: bool) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    counted, reason, gamble_line = amount, None, ""
    if ludoman:
        coef = round(random.uniform(0, 2), 2)
        counted = round(amount * coef)
        reason = f"🎰 лудомания: {amount} × {coef:.2f}"
        mood = "🤑 Казино в плюсе!" if coef > 1 else ("💀 Казино забрало всё." if counted == 0 else "📉 Казино в минусе.")
        gamble_line = f"🎰 Лудомания: {amount} × **{coef:.2f}** = **{counted}**. {mood}\n"
    # записываем и нулевой результат, иначе проигрыш не попадёт в баланс лудомании
    await add_entry(interaction.guild_id, interaction.user.id, "done", counted, interaction.user.id, reason)
    _, done_total, remaining = await user_stats(interaction.guild_id, interaction.user.id)
    text = gamble_line + f"✅ {interaction.user.mention} засчитано **{counted}** отжиманий (за год: {done_total})\n"
    text += f"Осталось: **{remaining}**" if remaining else "Долгов нет — красавчик! 🏆"
    await interaction.response.send_message(text)
    # возможно, теперь выполнены условия гражданства
    became, note = await try_naturalize(interaction.guild, interaction.user.id)
    if became:
        await interaction.followup.send(note)


@bot.tree.command(name="status", description="Сколько отжиманий осталось и сделано")
@app_commands.describe(user="Чей статус (по умолчанию — твой)")
async def status(interaction: discord.Interaction, user: discord.Member | None = None) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    target = user or interaction.user
    owed, done_total, remaining = await user_stats(interaction.guild_id, target.id)
    embed = discord.Embed(title=f"Отжимания: {target.display_name}", color=discord.Color.orange())
    embed.add_field(name="Осталось", value=str(remaining))
    embed.add_field(name="Сделано", value=str(done_total))
    embed.add_field(name="Назначено", value=str(owed))
    if await is_citizen(interaction.guild_id, target.id):
        civic = "❄️ гражданин (заморожен)" if remaining > FREEZE_DEBT else "🏛️ гражданин"
        if await is_president(interaction.guild_id, target.id):
            civic = "👑 президент, " + civic
    else:
        civic = "не гражданин"
    embed.add_field(name="Статус", value=civic, inline=False)
    embed.set_footer(text=f"За последние {WINDOW_DAYS} дней")
    await interaction.response.send_message(embed=embed)


@bot.tree.command(name="top", description="Лидерборд за последний год")
@app_commands.describe(board="Какой лидерборд показать")
@app_commands.choices(
    board=[
        app_commands.Choice(name="Больше всего осталось (должники)", value="remaining"),
        app_commands.Choice(name="Больше всего сделано", value="done"),
    ]
)
async def top(interaction: discord.Interaction, board: app_commands.Choice[str]) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    stats = await guild_stats(interaction.guild_id)
    if board.value == "remaining":
        scores = {uid: max(0, owed - d) for uid, (owed, d) in stats.items()}
        title = "😰 Должники: осталось отжиманий"
    else:
        scores = {uid: d for uid, (_, d) in stats.items()}
        title = "🏆 Сделано отжиманий"
    ranking = sorted(((s, uid) for uid, s in scores.items() if s > 0), reverse=True)[:TOP_SIZE]

    balance = await ludoman_balance(interaction.guild_id)
    medals = {1: "🥇", 2: "🥈", 3: "🥉"}
    lines = [
        f"{medals.get(i, f'`{i}.`')} <@{uid}> — **{score}**" + (f" ({balance[uid]:+d})" if uid in balance else "")
        for i, (score, uid) in enumerate(ranking, 1)
    ]
    embed = discord.Embed(
        title=title,
        description="\n".join(lines) or "Пока пусто.",
        color=discord.Color.gold(),
    )
    embed.set_footer(text=f"За последние {WINDOW_DAYS} дней · в скобках — баланс лудомании")
    await interaction.response.send_message(embed=embed)


@bot.tree.command(name="history", description="Последние записи участника")
@app_commands.describe(user="Чья история (по умолчанию — твоя)")
async def history(interaction: discord.Interaction, user: discord.Member | None = None) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    target = user or interaction.user
    rows = await bot.pool.fetch(
        "SELECT kind, amount, author_id, reason, created_at FROM entries "
        "WHERE guild_id = $1 AND user_id = $2 ORDER BY id DESC LIMIT 10",
        interaction.guild_id, target.id,
    )
    lines = []
    for r in rows:
        ts = int(r["created_at"].timestamp())
        if r["kind"] == "owe":
            line = f"<t:{ts}:d> ➕ {r['amount']} от <@{r['author_id']}>"
            if r["reason"]:
                line += f" — {r['reason']}"
        else:
            line = f"<t:{ts}:d> ✅ сделал {r['amount']}"
            if r["reason"]:
                line += f" ({r['reason']})"
        lines.append(line)
    embed = discord.Embed(
        title=f"История: {target.display_name}",
        description="\n".join(lines) or "Записей нет.",
        color=discord.Color.blurple(),
    )
    await interaction.response.send_message(embed=embed, ephemeral=True)


@bot.tree.command(name="undo", description="Отменить свою последнюю запись (если ошибся)")
async def undo(interaction: discord.Interaction) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    row = await bot.pool.fetchrow(
        "SELECT id, user_id, kind, amount, reason FROM entries "
        "WHERE guild_id = $1 AND author_id = $2 ORDER BY id DESC LIMIT 1",
        interaction.guild_id, interaction.user.id,
    )
    if row is None:
        await interaction.response.send_message("Тебе нечего отменять.", ephemeral=True)
        return
    if row["kind"] == "done" and (row["reason"] or "").startswith("🎰 лудомания:"):
        await interaction.response.send_message("🎰 Казино ставок не возвращает: бросок лудомании отменить нельзя.", ephemeral=True)
        return
    await bot.pool.execute("DELETE FROM entries WHERE id = $1", row["id"])
    if row["kind"] == "owe":
        what = f"назначение {row['amount']} отжиманий <@{row['user_id']}>"
    else:
        what = f"запись о {row['amount']} сделанных отжиманиях"
    await interaction.response.send_message(f"↩️ {interaction.user.mention} отменил {what}.")


# ---------- Dota 2 ----------


def kda_pushups(kills: int, deaths: int, assists: int) -> tuple[float, int]:
    """KDA = (убийства + ассисты) / смерти (смерти не меньше 1) → сколько отжиманий."""
    kda = (kills + assists) / max(deaths, 1)
    if kda >= 4:
        return kda, 0
    if kda >= 3:
        return kda, 10
    if kda >= 2:
        return kda, 20
    if kda >= 1:
        return kda, 30
    if kda > 0:
        return kda, 50
    return kda, 70


def parse_account_id(text: str) -> int | None:
    """Принимает Dota ID, Steam64 ID или ссылку на профиль Dotabuff/OpenDota/Steam (с числовым ID)."""
    numbers = re.findall(r"\d+", text)
    if not numbers:
        return None
    n = int(numbers[-1])
    if n >= STEAM64_BASE:
        n -= STEAM64_BASE
    return n if 0 < n < 2**32 else None


async def opendota(method: str, path: str, **params) -> object:
    if OPENDOTA_API_KEY:
        params["api_key"] = OPENDOTA_API_KEY
    async with bot.http_session.request(method, OPENDOTA + path, params=params) as resp:
        if resp.status >= 400:
            raise RuntimeError(f"OpenDota {resp.status} для {path}")
        return await resp.json(content_type=None)


async def process_new_matches(guild_id: int | None = None) -> tuple[list[dict], list[int]]:
    """Находит новые пати-матчи привязанных игроков и начисляет отжимания.

    Для каждого игрока считаются только матчи, начатые после привязки его аккаунта. Матч засчитывается,
    только если в нём играли минимум PARTY_MIN_LINKED участников одного сервера, привязанных к началу матча.
    Возвращает (обработанные матчи, user_id игроков, чьи матчи не удалось получить).
    """
    async with bot.dota_lock:
        if guild_id is None:
            links = await bot.pool.fetch("SELECT guild_id, user_id, account_id, linked_at, solo_since FROM dota_links")
        else:
            links = await bot.pool.fetch(
                "SELECT guild_id, user_id, account_id, linked_at, solo_since FROM dota_links WHERE guild_id = $1",
                guild_id,
            )
        failed: list[int] = []
        # (guild_id, match_id) -> [(link, match), ...]
        by_match: dict[tuple[int, int], list[tuple[asyncpg.Record, dict]]] = collections.defaultdict(list)

        for link in links:
            account_id = link["account_id"]
            try:
                # просим OpenDota подтянуть свежие матчи (появятся к следующей проверке)
                await opendota("POST", f"/players/{account_id}/refresh")
            except Exception:
                pass
            start_ts = int(link["linked_at"].timestamp())
            days = math.ceil((now_utc() - link["linked_at"]).total_seconds() / 86400) + 1
            try:
                # significant=0 — иначе OpenDota скрывает «незначимые» режимы, например Турбо
                matches = await opendota("GET", f"/players/{account_id}/matches", date=days, significant=0)
            except Exception as e:
                print(f"Не удалось получить матчи {account_id}: {e}")
                failed.append(link["user_id"])
                continue
            for m in matches:
                if m["start_time"] >= start_ts and (m.get("duration") or 0) >= MIN_MATCH_SECONDS:
                    by_match[(link["guild_id"], m["match_id"])].append((link, m))
            await asyncio.sleep(1)  # бережём лимиты OpenDota

        results: list[dict] = []
        for (gid, match_id), plays in sorted(by_match.items(), key=lambda kv: kv[1][0][1]["start_time"]):
            party = {link["user_id"] for link, _ in plays}
            is_party = len(party) >= PARTY_MIN_LINKED
            if not is_party:
                # соло-игра (или напарник ещё не появился на OpenDota). Считается только тем, кто включил
                # соло, после включения — и не раньше чем через SOLO_DELAY после конца матча,
                # чтобы пати-игра с запоздавшим напарником не записалась как соло.
                plays = [
                    (link, m) for link, m in plays
                    if link["solo_since"] is not None
                    and m["start_time"] >= link["solo_since"].timestamp()
                    and m["start_time"] + (m.get("duration") or 0) + SOLO_DELAY.total_seconds()
                    <= now_utc().timestamp()
                ]
            for link, m in plays:
                k, d, a = m["kills"], m["deaths"], m["assists"]
                kda, pushups = kda_pushups(k, d, a)
                played_at = dt.datetime.fromtimestamp(m["start_time"], dt.timezone.utc)
                async with bot.pool.acquire() as conn, conn.transaction():
                    inserted = await conn.fetchval(
                        "INSERT INTO dota_matches "
                        "(guild_id, user_id, match_id, kills, deaths, assists, pushups, played_at, is_party) "
                        "VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT DO NOTHING RETURNING match_id",
                        gid, link["user_id"], match_id, k, d, a, pushups, played_at, is_party,
                    )
                    if inserted is None:
                        continue  # уже обработан
                    if pushups:
                        await conn.execute(
                            "INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason, source) "
                            "VALUES ($1, $2, 'owe', $3, $4, $5, 'dota')",
                            gid, link["user_id"], pushups, bot.user.id,
                            f"Dota {k}/{d}/{a}, матч {match_id}" + ("" if is_party else " (соло)"),
                        )
                results.append({
                    "guild_id": gid, "user_id": link["user_id"], "match_id": match_id,
                    "k": k, "d": d, "a": a, "kda": kda, "pushups": pushups, "solo": not is_party,
                })
        return results, failed


def report_embeds(results: list[dict]) -> list[discord.Embed]:
    lines = []
    for r in results:
        verdict = f"**{r['pushups']}** отжиманий" if r["pushups"] else "без отжиманий 😎"
        lines.append(
            f"<@{r['user_id']}> — {r['k']}/{r['d']}/{r['a']}, KDA {r['kda']:.2f} → {verdict} "
            f"([матч](https://www.opendota.com/matches/{r['match_id']}))" + (" · соло" if r.get("solo") else "")
        )
    chunks, chunk = [], ""
    for line in lines:
        if len(chunk) + len(line) + 1 > 4000:
            chunks.append(chunk)
            chunk = ""
        chunk += line + "\n"
    if chunk:
        chunks.append(chunk)
    return [
        discord.Embed(title="🎮 Новые матчи Dota 2", description=text, color=discord.Color.red())
        for text in chunks[:10]
    ]


@tasks.loop(minutes=DOTA_CHECK_MINUTES)
async def dota_loop() -> None:
    try:
        results, _ = await process_new_matches()
        if not results or not DOTA_CHANNEL_ID:
            return
        channel = bot.get_channel(int(DOTA_CHANNEL_ID))
        if channel is None:
            print(f"Канал DOTA_CHANNEL_ID={DOTA_CHANNEL_ID} не найден")
            return
        results = [r for r in results if r["guild_id"] == channel.guild.id]
        for embed in report_embeds(results):
            await channel.send(embed=embed)
    except Exception as e:
        print(f"Ошибка автопроверки Dota: {e!r}")


@dota_loop.before_loop
async def before_dota_loop() -> None:
    await bot.wait_until_ready()


@bot.tree.command(name="linkdota", description="Привязать Dota-аккаунт к участнику")
@app_commands.describe(
    user="Участник Discord",
    account="Dota ID (Friend ID), Steam64 ID или ссылка на профиль Dotabuff/OpenDota",
)
async def linkdota(interaction: discord.Interaction, user: discord.Member, account: str) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    account_id = parse_account_id(account)
    if account_id is None:
        await interaction.response.send_message(
            "Не понял ID. Нужен числовой Dota ID / Steam64 ID или ссылка с ним.", ephemeral=True
        )
        return
    await interaction.response.defer()
    try:
        player = await opendota("GET", f"/players/{account_id}")
    except Exception:
        await interaction.followup.send("OpenDota сейчас не отвечает, попробуй позже.")
        return
    profile = player.get("profile") if isinstance(player, dict) else None
    if not profile:
        await interaction.followup.send(
            f"Не нашёл игрока `{account_id}` на OpenDota. Проверь ID и что в Dota 2 включено "
            "«Открыть общий доступ к данным матчей» (Expose Public Match Data)."
        )
        return
    linked_at = await bot.pool.fetchval(
        "INSERT INTO dota_links (guild_id, user_id, account_id, linked_by) VALUES ($1, $2, $3, $4) "
        "ON CONFLICT (guild_id, user_id) DO UPDATE SET account_id = EXCLUDED.account_id, "
        "linked_by = EXCLUDED.linked_by, "
        # перепривязка того же аккаунта не сдвигает начало учёта
        "linked_at = CASE WHEN dota_links.account_id = EXCLUDED.account_id THEN dota_links.linked_at ELSE now() END "
        "RETURNING linked_at",
        interaction.guild_id, user.id, account_id, interaction.user.id,
    )
    await interaction.followup.send(
        f"🔗 {user.mention} ↔ **{profile.get('personaname', '?')}** (`{account_id}`)\n"
        f"Считаются пати-матчи с <t:{int(linked_at.timestamp())}:f>."
    )


@bot.tree.command(name="unlinkdota", description="Отвязать Dota-аккаунт от участника")
@app_commands.describe(user="Участник Discord")
async def unlinkdota(interaction: discord.Interaction, user: discord.Member) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    deleted = await bot.pool.fetchval(
        "DELETE FROM dota_links WHERE guild_id = $1 AND user_id = $2 RETURNING account_id",
        interaction.guild_id, user.id,
    )
    if deleted is None:
        await interaction.response.send_message(f"У {user.mention} нет привязки.", ephemeral=True)
    else:
        await interaction.response.send_message(f"✂️ {user.mention} отвязан от `{deleted}`.")


@bot.tree.command(name="dotalinks", description="Список привязанных Dota-аккаунтов")
async def dotalinks(interaction: discord.Interaction) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    rows = await bot.pool.fetch(
        "SELECT user_id, account_id, solo_since FROM dota_links WHERE guild_id = $1 ORDER BY linked_at",
        interaction.guild_id,
    )
    lines = [
        f"<@{r['user_id']}> — [`{r['account_id']}`](https://www.opendota.com/players/{r['account_id']})"
        + (" · 🎯 соло" if r["solo_since"] else "")
        for r in rows
    ]
    embed = discord.Embed(
        title="🔗 Привязанные аккаунты", description="\n".join(lines) or "Пока никого.", color=discord.Color.red()
    )
    embed.set_footer(text=f"Автопроверка каждые {DOTA_CHECK_MINUTES} мин · только пати-игры")
    await interaction.response.send_message(embed=embed, ephemeral=True)


@bot.tree.command(name="calculate_dota", description="Посчитать новые пати-матчи Dota и начислить отжимания")
async def calculate_dota(interaction: discord.Interaction) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    await interaction.response.defer(thinking=True)
    results, failed = await process_new_matches(interaction.guild_id)
    note = ""
    if failed:
        note = "Не удалось получить матчи: " + ", ".join(f"<@{uid}>" for uid in failed)
    if not results:
        await interaction.followup.send("Новых пати-матчей нет." + ("\n" + note if note else ""))
        return
    await interaction.followup.send(content=note or None, embeds=report_embeds(results))


async def set_dota_solo(interaction: discord.Interaction, enabled: bool) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    solo_since = await bot.pool.fetchval(
        "UPDATE dota_links SET solo_since = CASE WHEN $3 THEN COALESCE(solo_since, now()) ELSE NULL END "
        "WHERE guild_id = $1 AND user_id = $2 RETURNING COALESCE(solo_since, 'epoch'::timestamptz)",
        interaction.guild_id, interaction.user.id, enabled,
    )
    if solo_since is None:
        await interaction.response.send_message("Сначала привяжи Dota-аккаунт: `/linkdota`.", ephemeral=True)
    elif enabled:
        await interaction.response.send_message(
            f"🎯 {interaction.user.mention} теперь получает отжимания и за **соло**-игры в Dota "
            f"(с <t:{int(solo_since.timestamp())}:f>)."
        )
    else:
        await interaction.response.send_message(
            f"👥 {interaction.user.mention} снова получает отжимания только за пати-игры в Dota."
        )


@bot.tree.command(name="dotasolo_on", description="Получать отжимания и за соло-игры в Dota")
async def dotasolo_on(interaction: discord.Interaction) -> None:
    await set_dota_solo(interaction, True)


@bot.tree.command(name="dotasolo_off", description="Снова получать отжимания только за пати-игры в Dota")
async def dotasolo_off(interaction: discord.Interaction) -> None:
    await set_dota_solo(interaction, False)


# ---------- PUBG ----------


def pubg_score(kills: int, assists: int, damage: float) -> tuple[int, int]:
    """Очки = 100 за килл + 50 за ассист + 1 за единицу урона → (очки, отжимания)."""
    points = kills * 100 + assists * 50 + round(damage)
    return points, (0 if points >= PUBG_SAFE_POINTS else PUBG_PUSHUPS)


class PubgNotFound(Exception):
    pass


async def pubg_api(path: str, **params) -> dict:
    headers = {"Authorization": f"Bearer {PUBG_API_KEY}", "Accept": "application/vnd.api+json"}
    async with bot.http_session.get(PUBG_API + path, params=params, headers=headers) as resp:
        if resp.status == 404:
            raise PubgNotFound(path)
        if resp.status >= 400:
            raise RuntimeError(f"PUBG API {resp.status} для {path}")
        return await resp.json(content_type=None)


async def process_new_pubg_matches(guild_id: int | None = None) -> tuple[list[dict], list[int]]:
    """Находит новые пати-матчи PUBG и начисляет отжимания.

    Матч засчитывается игроку, если он начался после привязки его аккаунта и в его команде было минимум
    PARTY_MIN_LINKED участников одного сервера, привязанных к началу матча.
    Возвращает (засчитанные результаты, user_id игроков, чьи матчи не удалось получить).
    """
    if not PUBG_API_KEY:
        return [], []
    async with bot.pubg_lock:
        if guild_id is None:
            links = await bot.pool.fetch("SELECT guild_id, user_id, account_id, name, linked_at FROM pubg_links")
        else:
            links = await bot.pool.fetch(
                "SELECT guild_id, user_id, account_id, name, linked_at FROM pubg_links WHERE guild_id = $1", guild_id
            )
        # account_id -> [link, ...] (один аккаунт может быть привязан на разных серверах)
        by_account: dict[str, list[asyncpg.Record]] = collections.defaultdict(list)
        for link in links:
            by_account[link["account_id"]].append(link)

        # недавние матчи игроков: до 10 аккаунтов за запрос (лимит — 10 запросов в минуту)
        failed: list[int] = []
        match_guilds: dict[str, set[int]] = collections.defaultdict(set)
        accounts = list(by_account)
        for i in range(0, len(accounts), 10):
            chunk = accounts[i:i + 10]
            try:
                data = await pubg_api("/players", **{"filter[playerIds]": ",".join(chunk)})
            except Exception as e:
                print(f"Не удалось получить матчи PUBG: {e}")
                failed += [link["user_id"] for acc in chunk for link in by_account[acc]]
                continue
            for player in data.get("data", []):
                for m in player.get("relationships", {}).get("matches", {}).get("data", []):
                    for link in by_account.get(player["id"], []):
                        match_guilds[m["id"]].add(link["guild_id"])

        results: list[dict] = []
        for match_id, guilds in match_guilds.items():
            checked = {r["guild_id"] for r in await bot.pool.fetch(
                "SELECT guild_id FROM pubg_checked WHERE match_id = $1 AND guild_id = ANY($2::bigint[])",
                match_id, list(guilds),
            )}
            todo = guilds - checked
            if not todo:
                continue
            try:
                match = await pubg_api(f"/matches/{match_id}")  # этот эндпоинт не лимитируется
            except Exception as e:
                print(f"Не удалось получить матч PUBG {match_id}: {e}")
                continue
            attrs = match["data"]["attributes"]
            played_at = dt.datetime.fromisoformat(attrs["createdAt"].replace("Z", "+00:00"))
            included = match.get("included", [])
            stats = {inc["id"]: inc["attributes"]["stats"] for inc in included if inc["type"] == "participant"}
            teams = [
                [stats[p["id"]] for p in inc["relationships"]["participants"]["data"] if p["id"] in stats]
                for inc in included if inc["type"] == "roster"
            ]
            for gid in todo:
                if attrs.get("matchType") in PUBG_MATCH_TYPES:
                    for team in teams:
                        members = [
                            (link, s) for s in team for link in by_account.get(s.get("playerId"), [])
                            if link["guild_id"] == gid and link["linked_at"] <= played_at
                        ]
                        if len({link["user_id"] for link, _ in members}) < PARTY_MIN_LINKED:
                            continue
                        for link, s in members:
                            kills, assists, damage = s.get("kills", 0), s.get("assists", 0), s.get("damageDealt", 0)
                            points, pushups = pubg_score(kills, assists, damage)
                            async with bot.pool.acquire() as conn, conn.transaction():
                                inserted = await conn.fetchval(
                                    "INSERT INTO pubg_matches (guild_id, user_id, match_id, kills, assists, damage, "
                                    "points, pushups, played_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) "
                                    "ON CONFLICT DO NOTHING RETURNING match_id",
                                    gid, link["user_id"], match_id, kills, assists, round(damage), points, pushups,
                                    played_at,
                                )
                                if inserted is None:
                                    continue
                                if pushups:
                                    await conn.execute(
                                        "INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason, source) "
                                        "VALUES ($1, $2, 'owe', $3, $4, $5, 'pubg')",
                                        gid, link["user_id"], pushups, bot.user.id,
                                        f"PUBG {kills} кил., {assists} асс., {round(damage)} урона = {points} очк.",
                                    )
                            results.append({
                                "guild_id": gid, "user_id": link["user_id"], "kills": kills, "assists": assists,
                                "damage": round(damage), "points": points, "pushups": pushups,
                                "mode": attrs.get("gameMode", "?"), "played_at": played_at,
                            })
                await bot.pool.execute(
                    "INSERT INTO pubg_checked (guild_id, match_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", gid, match_id
                )
        results.sort(key=lambda r: r["played_at"])
        return results, failed


def pubg_report_embeds(results: list[dict]) -> list[discord.Embed]:
    lines = []
    for r in results:
        verdict = f"**{r['pushups']}** отжиманий" if r["pushups"] else "без отжиманий 😎"
        lines.append(
            f"<@{r['user_id']}> — {r['kills']} кил., {r['assists']} асс., {r['damage']} урона "
            f"= {r['points']} очков → {verdict} ({r['mode']})"
        )
    chunks, chunk = [], ""
    for line in lines:
        if len(chunk) + len(line) + 1 > 4000:
            chunks.append(chunk)
            chunk = ""
        chunk += line + "\n"
    if chunk:
        chunks.append(chunk)
    return [
        discord.Embed(title="🔫 Новые матчи PUBG", description=text, color=discord.Color.dark_orange())
        for text in chunks[:10]
    ]


@tasks.loop(minutes=DOTA_CHECK_MINUTES)
async def pubg_loop() -> None:
    try:
        results, _ = await process_new_pubg_matches()
        if not results or not DOTA_CHANNEL_ID:
            return
        channel = bot.get_channel(int(DOTA_CHANNEL_ID))
        if channel is None:
            return
        results = [r for r in results if r["guild_id"] == channel.guild.id]
        for embed in pubg_report_embeds(results):
            await channel.send(embed=embed)
    except Exception as e:
        print(f"Ошибка автопроверки PUBG: {e!r}")


@pubg_loop.before_loop
async def before_pubg_loop() -> None:
    await bot.wait_until_ready()


@bot.tree.command(name="linkpubg", description="Привязать PUBG-аккаунт к участнику")
@app_commands.describe(user="Участник Discord", nickname="Ник в PUBG (с учётом регистра)")
async def linkpubg(interaction: discord.Interaction, user: discord.Member, nickname: str) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    if not PUBG_API_KEY:
        await interaction.response.send_message("PUBG не настроен: не задан PUBG_API_KEY.", ephemeral=True)
        return
    await interaction.response.defer()
    try:
        data = await pubg_api("/players", **{"filter[playerNames]": nickname.strip()})
    except PubgNotFound:
        await interaction.followup.send(f"Игрок `{nickname}` не найден в PUBG ({PUBG_SHARD}). Ник чувствителен к регистру.")
        return
    except Exception:
        await interaction.followup.send("PUBG API сейчас не отвечает, попробуй через минуту.")
        return
    player = data["data"][0]
    name = player["attributes"]["name"]
    linked_at = await bot.pool.fetchval(
        "INSERT INTO pubg_links (guild_id, user_id, account_id, name, linked_by) VALUES ($1, $2, $3, $4, $5) "
        "ON CONFLICT (guild_id, user_id) DO UPDATE SET account_id = EXCLUDED.account_id, name = EXCLUDED.name, "
        "linked_by = EXCLUDED.linked_by, "
        # перепривязка того же аккаунта не сдвигает начало учёта
        "linked_at = CASE WHEN pubg_links.account_id = EXCLUDED.account_id THEN pubg_links.linked_at ELSE now() END "
        "RETURNING linked_at",
        interaction.guild_id, user.id, player["id"], name, interaction.user.id,
    )
    await interaction.followup.send(
        f"🔗 {user.mention} ↔ **{name}** (PUBG)\nСчитаются пати-матчи с <t:{int(linked_at.timestamp())}:f>."
    )


@bot.tree.command(name="unlinkpubg", description="Отвязать PUBG-аккаунт от участника")
@app_commands.describe(user="Участник Discord")
async def unlinkpubg(interaction: discord.Interaction, user: discord.Member) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    name = await bot.pool.fetchval(
        "DELETE FROM pubg_links WHERE guild_id = $1 AND user_id = $2 RETURNING name", interaction.guild_id, user.id
    )
    if name is None:
        await interaction.response.send_message(f"У {user.mention} нет привязки PUBG.", ephemeral=True)
    else:
        await interaction.response.send_message(f"✂️ {user.mention} отвязан от PUBG-аккаунта **{name}**.")


@bot.tree.command(name="pubglinks", description="Список привязанных PUBG-аккаунтов")
async def pubglinks(interaction: discord.Interaction) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    rows = await bot.pool.fetch(
        "SELECT user_id, name FROM pubg_links WHERE guild_id = $1 ORDER BY linked_at", interaction.guild_id
    )
    embed = discord.Embed(
        title="🔗 Привязанные PUBG-аккаунты",
        description="\n".join(f"<@{r['user_id']}> — **{r['name']}**" for r in rows) or "Пока никого.",
        color=discord.Color.dark_orange(),
    )
    embed.set_footer(text=f"Автопроверка каждые {DOTA_CHECK_MINUTES} мин · только пати-игры")
    await interaction.response.send_message(embed=embed, ephemeral=True)


@bot.tree.command(name="calculate_pubg", description="Посчитать новые пати-матчи PUBG и начислить отжимания")
async def calculate_pubg(interaction: discord.Interaction) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    if not PUBG_API_KEY:
        await interaction.response.send_message("PUBG не настроен: не задан PUBG_API_KEY.", ephemeral=True)
        return
    await interaction.response.defer(thinking=True)
    results, failed = await process_new_pubg_matches(interaction.guild_id)
    note = ""
    if failed:
        note = "Не удалось получить матчи (возможно, лимит PUBG API — попробуй через минуту): " + ", ".join(
            f"<@{uid}>" for uid in failed
        )
    if not results:
        await interaction.followup.send("Новых пати-матчей PUBG нет." + ("\n" + note if note else ""))
        return
    await interaction.followup.send(content=note or None, embeds=pubg_report_embeds(results))


# ---------- гражданство: команды ----------


async def try_naturalize(guild: discord.Guild | None, user_id: int) -> tuple[bool, str]:
    """Делает гражданином, если есть поручители и выполнены условия. Возвращает (стал ли, пояснение)."""
    if guild is None or await is_citizen(guild.id, user_id):
        return False, ""
    rows = await bot.pool.fetch(
        "SELECT voucher_id FROM vouches WHERE guild_id = $1 AND candidate_id = $2", guild.id, user_id
    )
    citizens = set(await citizen_ids(guild.id))
    vouchers = [r["voucher_id"] for r in rows if r["voucher_id"] in citizens]
    if len(vouchers) < VOUCHES_NEEDED:
        return False, f"Поручителей: {len(vouchers)}/{VOUCHES_NEEDED}."
    _, done_total, remaining = await user_stats(guild.id, user_id)
    if remaining >= CITIZEN_MAX_DEBT:
        return False, f"Поручителей хватает, но долг {remaining} — нужно меньше {CITIZEN_MAX_DEBT}."
    if done_total == 0:
        return False, "Поручителей хватает, но не сделано ни одного отжимания."
    await bot.pool.execute(
        "INSERT INTO citizens (guild_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", guild.id, user_id
    )
    await bot.pool.execute("DELETE FROM vouches WHERE guild_id = $1 AND candidate_id = $2", guild.id, user_id)
    await sync_roles(guild)
    return True, f"🎉 <@{user_id}> стал гражданином!"


@bot.tree.command(name="founder", description="(Админ) Назначить гражданина-основателя")
@app_commands.describe(user="Кого сделать гражданином")
@app_commands.default_permissions(manage_guild=True)
@app_commands.guild_only()
async def founder(interaction: discord.Interaction, user: discord.Member) -> None:
    if not interaction.user.guild_permissions.manage_guild:
        await interaction.response.send_message("Только для админов сервера.", ephemeral=True)
        return
    if user.bot:
        await interaction.response.send_message("Боты не граждане 🤖", ephemeral=True)
        return
    await interaction.response.defer()
    await bot.pool.execute(
        "INSERT INTO citizens (guild_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", interaction.guild_id, user.id
    )
    await sync_roles(interaction.guild)
    await interaction.followup.send(f"🏛️ {user.mention} — гражданин-основатель.")


@bot.tree.command(name="vouch", description="Поручиться за участника, чтобы он стал гражданином")
@app_commands.describe(user="За кого ручаешься")
async def vouch(interaction: discord.Interaction, user: discord.Member) -> None:
    if not await require_active_citizen(interaction):
        return
    if user.bot or user.id == interaction.user.id:
        await interaction.response.send_message("За себя и за ботов ручаться нельзя.", ephemeral=True)
        return
    if await is_citizen(interaction.guild_id, user.id):
        await interaction.response.send_message(f"{user.mention} уже гражданин.", ephemeral=True)
        return
    if user.joined_at is None or now_utc() - user.joined_at < dt.timedelta(days=CITIZEN_MIN_DAYS):
        allowed = f" Можно будет <t:{int((user.joined_at + dt.timedelta(days=CITIZEN_MIN_DAYS)).timestamp())}:R>." \
            if user.joined_at else ""
        await interaction.response.send_message(
            f"{user.mention} на сервере меньше {CITIZEN_MIN_DAYS} дней — ручаться пока рано.{allowed}", ephemeral=True
        )
        return
    await interaction.response.defer()
    await bot.pool.execute(
        "INSERT INTO vouches (guild_id, candidate_id, voucher_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING",
        interaction.guild_id, user.id, interaction.user.id,
    )
    became, note = await try_naturalize(interaction.guild, user.id)
    text = f"🤝 {interaction.user.mention} поручился за {user.mention}."
    await interaction.followup.send(text + "\n" + note)


@bot.tree.command(name="citizens", description="Список граждан")
async def citizens_cmd(interaction: discord.Interaction) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    gid = interaction.guild_id
    ids = await citizen_ids(gid)
    active = set(await active_citizens(gid))
    president = await current_president(gid)
    marginals = {r["user_id"] for r in await bot.pool.fetch("SELECT user_id FROM marginals WHERE guild_id = $1", gid)}
    lines = []
    for uid in ids:
        marks = ""
        if president and president["user_id"] == uid:
            marks += " 👑"
        if uid not in active:
            marks += " ❄️"
        if uid in marginals:
            marks += " 🚫"
        lines.append(f"<@{uid}>{marks}")
    embed = discord.Embed(
        title=f"🏛️ Граждане ({len(ids)})", description="\n".join(lines) or "Пока никого.", color=discord.Color.blue()
    )
    embed.set_footer(text="👑 президент · ❄️ заморожен · 🚫 маргинал")
    await interaction.response.send_message(embed=embed, ephemeral=True)


@bot.tree.command(name="laws", description="Конституция и принятые законы")
async def laws_cmd(interaction: discord.Interaction) -> None:
    if interaction.guild_id is None:
        await interaction.response.send_message(no_guild_reply(), ephemeral=True)
        return
    rows = await bot.pool.fetch(
        "SELECT id, text, adopted_at FROM laws WHERE guild_id = $1 ORDER BY adopted_at", interaction.guild_id
    )
    embed = discord.Embed(title="📜 Законы", color=discord.Color.dark_gold())
    embed.add_field(
        name="Конституция",
        value="\n".join(f"**{i}.** {text}" for i, text in enumerate(CONSTITUTION, 1)),
        inline=False,
    )
    laws_text = "\n".join(
        f"**{i}.** {r['text']} (<t:{int(r['adopted_at'].timestamp())}:d>)" for i, r in enumerate(rows, 1)
    )
    embed.description = laws_text[:4000] if laws_text else "Принятых законов пока нет."
    await interaction.response.send_message(embed=embed)


# ---------- голосования ----------

KIND_TITLES = {
    "revoke": "🗳️ Лишение гражданства",
    "accuse": "⚖️ Суд",
    "law": "📜 Референдум",
    "marginal": "🚫 Признание маргиналом",
    "election": "👑 Выборы президента",
}
TARGETED_KINDS = {"revoke", "accuse", "marginal"}  # по своему делу голосовать нельзя


def poll_description(p: asyncpg.Record) -> str:
    kind = p["kind"]
    if kind == "revoke":
        return (f"Лишить <@{p['target_id']}> гражданства?\n"
                "Нужно, чтобы «за» проголосовали ⅔ всех граждан (кроме замороженных и самого обвиняемого).")
    if kind == "accuse":
        lawyer = f"<@{p['lawyer_id']}>" if p["lawyer_id"] else "не нашлось"
        return (f"<@{p['creator_id']}> обвиняет <@{p['target_id']}>: {p['text']}\n"
                f"Наказание: **{p['amount']}** отжиманий\nАдвокат: {lawyer}\n\n"
                "Виновен, если «за» проголосовали больше половины граждан (кроме обвиняемого). "
                f"Иначе **{p['amount']}** отжиманий получает обвинитель за клевету.")
    if kind == "law":
        return f"<@{p['creator_id']}> предлагает закон:\n> {p['text']}\n\nПринимается простым большинством голосов."
    if kind == "marginal":
        return (f"Признать <@{p['target_id']}> маргиналом? Он не будет участвовать в ближайшей выдаче наград.\n"
                "Нужно, чтобы «за» проголосовали больше половины граждан (кроме него самого).")
    if p["status"] == "nominating":
        return f"Граждане, выдвигайтесь командой `/nominate` до <t:{int(p['ends_at'].timestamp())}:f>."
    return "Голосуй за кандидата в меню ниже. Побеждает тот, у кого больше голосов."


async def poll_embed(p: asyncpg.Record) -> discord.Embed:
    embed = discord.Embed(
        title=f"{KIND_TITLES[p['kind']]} #{p['id']}", description=poll_description(p), color=discord.Color.purple()
    )
    rows = await bot.pool.fetch(
        "SELECT choice, COUNT(*) AS n FROM poll_votes WHERE poll_id = $1 GROUP BY choice", p["id"]
    )
    tally = {r["choice"]: r["n"] for r in rows}
    if p["kind"] == "election":
        cands = await bot.pool.fetch("SELECT user_id FROM election_candidates WHERE poll_id = $1", p["id"])
        if p["status"] == "nominating":
            value = "\n".join(f"<@{c['user_id']}>" for c in cands) or "пока никого"
            embed.add_field(name="Кандидаты", value=value, inline=False)
        else:
            value = "\n".join(f"<@{c['user_id']}> — {tally.get(str(c['user_id']), 0)}" for c in cands) or "—"
            embed.add_field(name="Голоса", value=value, inline=False)
    else:
        embed.add_field(name="✅ За", value=str(tally.get("yes", 0)))
        embed.add_field(name="❌ Против", value=str(tally.get("no", 0)))
    if p["status"] in ("open", "nominating"):
        embed.add_field(name="Итог", value=f"<t:{int(p['ends_at'].timestamp())}:R>", inline=False)
    else:
        embed.add_field(name="Итог", value=p["result"] or "—", inline=False)
    return embed


def yes_no_view(poll_id: int) -> discord.ui.View:
    view = discord.ui.View(timeout=None)
    view.add_item(discord.ui.Button(
        label="За", emoji="✅", style=discord.ButtonStyle.success, custom_id=f"vote:{poll_id}:yes"
    ))
    view.add_item(discord.ui.Button(
        label="Против", emoji="❌", style=discord.ButtonStyle.danger, custom_id=f"vote:{poll_id}:no"
    ))
    return view


def election_view(poll_id: int, candidates: list[tuple[int, str]]) -> discord.ui.View:
    view = discord.ui.View(timeout=None)
    view.add_item(discord.ui.Select(
        custom_id=f"elect:{poll_id}",
        placeholder="Голосовать за…",
        options=[discord.SelectOption(label=name[:100], value=str(uid)) for uid, name in candidates[:25]],
    ))
    return view


async def open_poll_exists(guild_id: int, kind: str, target_id: int | None = None) -> bool:
    return bool(await bot.pool.fetchval(
        "SELECT 1 FROM polls WHERE guild_id = $1 AND kind = $2 AND status IN ('open', 'nominating') "
        "AND target_id IS NOT DISTINCT FROM $3",
        guild_id, kind, target_id,
    ))


async def create_poll(
    interaction: discord.Interaction, kind: str, hours: int, *, status: str = "open",
    target_id: int | None = None, lawyer_id: int | None = None, amount: int | None = None, text: str | None = None,
) -> None:
    ends_at = now_utc() + dt.timedelta(hours=hours)
    poll_id = await bot.pool.fetchval(
        "INSERT INTO polls (guild_id, kind, status, creator_id, target_id, lawyer_id, amount, text, ends_at) "
        "VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id",
        interaction.guild_id, kind, status, interaction.user.id, target_id, lawyer_id, amount, text, ends_at,
    )
    p = await bot.pool.fetchrow("SELECT * FROM polls WHERE id = $1", poll_id)
    view = yes_no_view(poll_id) if status == "open" else discord.utils.MISSING
    await interaction.response.send_message(embed=await poll_embed(p), view=view)
    msg = await interaction.original_response()
    await bot.pool.execute(
        "UPDATE polls SET channel_id = $1, message_id = $2 WHERE id = $3", msg.channel.id, msg.id, poll_id
    )


async def handle_vote(interaction: discord.Interaction, poll_id: int, choice: str) -> None:
    p = await bot.pool.fetchrow("SELECT * FROM polls WHERE id = $1", poll_id)
    if p is None or p["status"] != "open" or p["ends_at"] <= now_utc():
        await interaction.response.send_message("Голосование уже закрыто.", ephemeral=True)
        return
    if not await require_active_citizen(interaction):
        return
    if p["kind"] in TARGETED_KINDS and interaction.user.id == p["target_id"]:
        await interaction.response.send_message("По своему делу голосовать нельзя.", ephemeral=True)
        return
    if p["kind"] == "election":
        ok = await bot.pool.fetchval(
            "SELECT 1 FROM election_candidates WHERE poll_id = $1 AND user_id = $2", poll_id, int(choice)
        )
        if not ok:
            await interaction.response.send_message("Такого кандидата нет.", ephemeral=True)
            return
    elif choice not in ("yes", "no"):
        return
    # голос окончательный: повторное нажатие не меняет его
    inserted = await bot.pool.fetchval(
        "INSERT INTO poll_votes (poll_id, voter_id, choice) VALUES ($1, $2, $3) "
        "ON CONFLICT (poll_id, voter_id) DO NOTHING RETURNING choice",
        poll_id, interaction.user.id, choice,
    )
    if inserted is None:
        await interaction.response.send_message("Ты уже проголосовал — голос менять нельзя.", ephemeral=True)
        return
    await interaction.response.edit_message(embed=await poll_embed(p))
    await interaction.followup.send("Голос учтён ✅ Изменить его уже нельзя.", ephemeral=True)


async def announce_poll(p: asyncpg.Record, note: str) -> None:
    """Обновляет сообщение голосования (убирает кнопки) и пишет итог ответом на него."""
    channel = bot.get_channel(p["channel_id"]) if p["channel_id"] else None
    if channel is None:
        return
    embed = await poll_embed(p)
    try:
        msg = await channel.fetch_message(p["message_id"])
        await msg.edit(embed=embed, view=None)
        await msg.reply(note)
    except discord.HTTPException:
        await channel.send(note, embed=embed)


async def resolve_poll(p: asyncpg.Record) -> None:
    gid, kind, target, amount = p["guild_id"], p["kind"], p["target_id"], p["amount"]
    voters = {u for u in await active_citizens(gid) if not (kind in TARGETED_KINDS and u == target)}
    rows = await bot.pool.fetch("SELECT voter_id, choice FROM poll_votes WHERE poll_id = $1", p["id"])
    votes = {r["voter_id"]: r["choice"] for r in rows if r["voter_id"] in voters}  # только действующие голоса
    yes = sum(1 for c in votes.values() if c == "yes")
    no = sum(1 for c in votes.values() if c == "no")
    n = len(voters)
    title = f"{KIND_TITLES[kind]} #{p['id']}"

    if kind == "revoke":
        passed = n > 0 and yes * 3 >= 2 * n
        if passed:
            await bot.pool.execute("DELETE FROM citizens WHERE guild_id = $1 AND user_id = $2", gid, target)
            result = f"<@{target}> лишён гражданства ({yes} из {n} за)."
        else:
            result = f"<@{target}> остаётся гражданином ({yes} из {n} за, нужно ⅔)."
    elif kind == "accuse":
        passed = yes * 2 > n
        punished = target if passed else p["creator_id"]
        reason = f"Суд #{p['id']}: " + ("виновен" if passed else "клевета")
        await add_entry(gid, punished, "owe", amount, bot.user.id, reason, "court")
        if passed:
            result = f"Виновен ({yes} из {n} за). <@{target}> получает **{amount}** отжиманий."
        else:
            result = f"Не виновен ({yes} из {n} за). <@{p['creator_id']}> получает **{amount}** отжиманий за клевету."
    elif kind == "marginal":
        passed = yes * 2 > n
        if passed:
            await bot.pool.execute(
                "INSERT INTO marginals (guild_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING", gid, target
            )
            result = f"<@{target}> признан маргиналом ({yes} из {n} за) и пропустит ближайшую выдачу наград."
        else:
            result = f"<@{target}> не маргинал ({yes} из {n} за)."
    elif kind == "law":
        passed = yes > no
        if passed:
            await bot.pool.execute(
                "INSERT INTO laws (guild_id, text, poll_id) VALUES ($1, $2, $3)", gid, p["text"], p["id"]
            )
            result = f"Закон принят ({yes} за / {no} против). Смотри `/laws`."
        else:
            result = f"Закон отклонён ({yes} за / {no} против)."
    else:  # election
        tally = collections.Counter(votes.values())
        passed = bool(tally)
        if passed:
            best = max(tally.values())
            leaders = [int(c) for c, v in tally.items() if v == best]
            winner = random.choice(leaders)
            await bot.pool.execute("INSERT INTO presidents (guild_id, user_id) VALUES ($1, $2)", gid, winner)
            result = f"Новый президент — <@{winner}> ({best} голосов)" + (" — по жребию среди равных." if len(leaders) > 1 else ".")
        else:
            result = "Никто не проголосовал, президент не выбран."

    await bot.pool.execute(
        "UPDATE polls SET status = $1, result = $2 WHERE id = $3", "passed" if passed else "failed", result, p["id"]
    )
    guild = bot.get_guild(gid)
    if guild:
        await sync_roles(guild)
    p = await bot.pool.fetchrow("SELECT * FROM polls WHERE id = $1", p["id"])
    await announce_poll(p, f"**{title}:** {result}")


async def start_election_voting(p: asyncpg.Record) -> None:
    cands = [r["user_id"] for r in await bot.pool.fetch(
        "SELECT user_id FROM election_candidates WHERE poll_id = $1", p["id"]
    )]
    if not cands:
        await bot.pool.execute(
            "UPDATE polls SET status = 'failed', result = 'Никто не выдвинулся.' WHERE id = $1", p["id"]
        )
        p = await bot.pool.fetchrow("SELECT * FROM polls WHERE id = $1", p["id"])
        await announce_poll(p, f"**{KIND_TITLES['election']} #{p['id']}:** никто не выдвинулся.")
        return
    await bot.pool.execute(
        "UPDATE polls SET status = 'open', ends_at = $1 WHERE id = $2",
        now_utc() + dt.timedelta(hours=ELECTION_PHASE_HOURS), p["id"],
    )
    p = await bot.pool.fetchrow("SELECT * FROM polls WHERE id = $1", p["id"])
    channel = bot.get_channel(p["channel_id"]) if p["channel_id"] else None
    guild = bot.get_guild(p["guild_id"])
    if channel is None or guild is None:
        return
    try:
        old = await channel.fetch_message(p["message_id"])
        await old.edit(view=None)
    except discord.HTTPException:
        pass
    names = [(uid, await member_name(guild, uid)) for uid in cands]
    msg = await channel.send("🗳️ Выдвижение окончено — голосуем!", embed=await poll_embed(p),
                             view=election_view(p["id"], names))
    await bot.pool.execute(
        "UPDATE polls SET channel_id = $1, message_id = $2 WHERE id = $3", channel.id, msg.id, p["id"]
    )


@tasks.loop(minutes=1)
async def polls_loop() -> None:
    try:
        rows = await bot.pool.fetch(
            "SELECT * FROM polls WHERE status IN ('open', 'nominating') AND ends_at <= now() ORDER BY ends_at"
        )
        for p in rows:
            if p["status"] == "nominating":
                await start_election_voting(p)
            else:
                await resolve_poll(p)
    except Exception as e:
        print(f"Ошибка подведения итогов голосований: {e!r}")


@polls_loop.before_loop
async def before_polls_loop() -> None:
    await bot.wait_until_ready()


Hours = app_commands.Range[int, 1, 168]


@bot.tree.command(name="accuse", description="Обвинить участника (решают граждане)")
@app_commands.describe(
    user="Кого обвиняешь", pushups="Наказание в отжиманиях", reason="В чём обвинение",
    hours="Сколько часов идёт голосование (по умолчанию 24)",
)
async def accuse(
    interaction: discord.Interaction, user: discord.Member, pushups: app_commands.Range[int, 1, 1000],
    reason: str, hours: Hours = 24,
) -> None:
    if not await require_active_citizen(interaction):
        return
    gid = interaction.guild_id
    if user.bot or user.id == interaction.user.id:
        await interaction.response.send_message("Себя и ботов обвинять нельзя.", ephemeral=True)
        return
    if await is_president(gid, user.id):
        await interaction.response.send_message("🛡️ Президента нельзя забанить (Конституция, ст. 3).", ephemeral=True)
        return
    candidates = [u for u in await active_citizens(gid) if u not in (interaction.user.id, user.id)]
    lawyer = random.choice(candidates) if candidates else None
    await create_poll(interaction, "accuse", hours, target_id=user.id, lawyer_id=lawyer, amount=pushups, text=reason)


@bot.tree.command(name="revoke", description="Голосование за лишение гражданства (нужно ⅔ граждан)")
@app_commands.describe(user="Кого лишить гражданства", hours="Сколько часов идёт голосование (по умолчанию 24)")
async def revoke(interaction: discord.Interaction, user: discord.Member, hours: Hours = 24) -> None:
    if not await require_active_citizen(interaction):
        return
    gid = interaction.guild_id
    if not await is_citizen(gid, user.id):
        await interaction.response.send_message(f"{user.mention} не гражданин.", ephemeral=True)
        return
    if await is_president(gid, user.id):
        await interaction.response.send_message("🛡️ Президента нельзя забанить (Конституция, ст. 3).", ephemeral=True)
        return
    if await open_poll_exists(gid, "revoke", user.id):
        await interaction.response.send_message("Такое голосование уже идёт.", ephemeral=True)
        return
    await create_poll(interaction, "revoke", hours, target_id=user.id)


@bot.tree.command(name="marginal", description="Голосование: признать гражданина маргиналом (без наград)")
@app_commands.describe(user="Кого признать маргиналом", hours="Сколько часов идёт голосование (по умолчанию 24)")
async def marginal(interaction: discord.Interaction, user: discord.Member, hours: Hours = 24) -> None:
    if not await require_active_citizen(interaction):
        return
    gid = interaction.guild_id
    if not await is_citizen(gid, user.id):
        await interaction.response.send_message(
            f"{user.mention} не гражданин — в наградах он и так не участвует.", ephemeral=True
        )
        return
    if await is_president(gid, user.id):
        await interaction.response.send_message("🛡️ Президента нельзя забанить (Конституция, ст. 3).", ephemeral=True)
        return
    if await open_poll_exists(gid, "marginal", user.id):
        await interaction.response.send_message("Такое голосование уже идёт.", ephemeral=True)
        return
    await create_poll(interaction, "marginal", hours, target_id=user.id)


@bot.tree.command(name="propose", description="Предложить закон (референдум на 1 день)")
@app_commands.describe(text="Текст закона")
async def propose(interaction: discord.Interaction, text: app_commands.Range[str, 5, 1000]) -> None:
    if not await require_active_citizen(interaction):
        return
    await create_poll(interaction, "law", LAW_HOURS, text=text)


@bot.tree.command(name="election", description="Начать выборы президента")
async def election(interaction: discord.Interaction) -> None:
    if not await require_active_citizen(interaction):
        return
    gid = interaction.guild_id
    if await open_poll_exists(gid, "election"):
        await interaction.response.send_message("Выборы уже идут.", ephemeral=True)
        return
    president = await current_president(gid)
    if president:
        allowed_at = president["elected_at"] + dt.timedelta(days=PRESIDENT_TERM_DAYS)
        if now_utc() < allowed_at:
            await interaction.response.send_message(
                f"Срок президента <@{president['user_id']}> ещё не истёк. "
                f"Перевыборы можно начать <t:{int(allowed_at.timestamp())}:R>.",
                ephemeral=True,
            )
            return
    await create_poll(interaction, "election", ELECTION_PHASE_HOURS, status="nominating")


@bot.tree.command(name="nominate", description="Выдвинуть себя в президенты (во время выборов)")
async def nominate(interaction: discord.Interaction) -> None:
    if not await require_active_citizen(interaction):
        return
    poll_id = await bot.pool.fetchval(
        "SELECT id FROM polls WHERE guild_id = $1 AND kind = 'election' AND status = 'nominating'",
        interaction.guild_id,
    )
    if poll_id is None:
        await interaction.response.send_message("Сейчас нет выдвижения. Начать выборы: `/election`.", ephemeral=True)
        return
    await bot.pool.execute(
        "INSERT INTO election_candidates (poll_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING",
        poll_id, interaction.user.id,
    )
    await interaction.response.send_message(f"🎙️ {interaction.user.mention} баллотируется в президенты!")
    p = await bot.pool.fetchrow("SELECT * FROM polls WHERE id = $1", poll_id)
    channel = bot.get_channel(p["channel_id"]) if p["channel_id"] else None
    if channel:
        try:
            msg = await channel.fetch_message(p["message_id"])
            await msg.edit(embed=await poll_embed(p))
        except discord.HTTPException:
            pass


# ---------- роли по итогам и награды ----------


async def period_scores(guild_id: int, start: dt.datetime, end: dt.datetime) -> tuple[dict[int, int], dict[int, int]]:
    """(назначено за матчи Dota/PUBG и суд, число пати-игр Dota + PUBG) за период [start, end)."""
    scores: dict[int, int] = collections.defaultdict(int)
    games: dict[int, int] = collections.defaultdict(int)
    for table in ("dota_matches", "pubg_matches"):
        for r in await bot.pool.fetch(
            f"SELECT user_id, SUM(pushups) AS s, COUNT(*) AS g FROM {table} "
            "WHERE guild_id = $1 AND played_at >= $2 AND played_at < $3 GROUP BY user_id",
            guild_id, start, end,
        ):
            scores[r["user_id"]] += r["s"]
            games[r["user_id"]] += r["g"]
    for r in await bot.pool.fetch(
        "SELECT user_id, SUM(amount) AS s FROM entries WHERE guild_id = $1 AND kind = 'owe' AND source = 'court' "
        "AND created_at >= $2 AND created_at < $3 GROUP BY user_id",
        guild_id, start, end,
    ):
        scores[r["user_id"]] += r["s"]
    return scores, games


async def eligible_ranking(
    guild_id: int, start: dt.datetime, end: dt.datetime, skip_marginals: bool
) -> list[tuple[int, int]]:
    """[(user_id, назначено)] граждан без заморозки, сыгравших больше AWARD_MIN_GAMES пати-игр."""
    scores, games = await period_scores(guild_id, start, end)
    users = await active_citizens(guild_id)
    if skip_marginals:
        marginals = {r["user_id"] for r in await bot.pool.fetch(
            "SELECT user_id FROM marginals WHERE guild_id = $1", guild_id
        )}
        users = [u for u in users if u not in marginals]
    return [(u, scores.get(u, 0)) for u in users if games.get(u, 0) > AWARD_MIN_GAMES]


def leaders(ranking: list[tuple[int, int]], best: bool) -> tuple[list[int], int]:
    value = (max if best else min)(s for _, s in ranking)
    return [u for u, s in ranking if s == value], value


async def sync_rank_roles(guild: discord.Guild) -> None:
    """Топ руинер и Порядочный гражданин — по назначенным отжиманиям за последние 30 дней."""
    end = now_utc()
    ranking = await eligible_ranking(guild.id, end - dt.timedelta(days=30), end, skip_marginals=False)
    if len(ranking) < 2:
        await set_role_holders(guild, "ruiner", set())
        await set_role_holders(guild, "decent", set())
        return
    top_ids, _ = leaders(ranking, best=True)
    bottom_ids, _ = leaders(ranking, best=False)
    await set_role_holders(guild, "ruiner", set(top_ids))
    await set_role_holders(guild, "decent", set(bottom_ids))


def add_months(d: dt.datetime, months: int) -> dt.datetime:
    total = d.year * 12 + d.month - 1 + months
    return d.replace(year=total // 12, month=total % 12 + 1)


async def award_text(guild_id: int, start: dt.datetime, end: dt.datetime, prize: str) -> str:
    ranking = await eligible_ranking(guild_id, start, end, skip_marginals=True)
    if not ranking:
        return f"Никто из граждан не сыграл больше {AWARD_MIN_GAMES} пати-игр — {prize} никому."
    top_ids, top_value = leaders(ranking, best=True)
    bottom_ids, bottom_value = leaders(ranking, best=False)
    mention = lambda ids: ", ".join(f"<@{u}>" for u in ids)  # noqa: E731
    return (f"😈 Топ-1 (больше всех назначено, {top_value}): {mention(top_ids)} — {prize}\n"
            f"😇 Боттом-1 (меньше всех назначено, {bottom_value}): {mention(bottom_ids)} — {prize}")


async def maybe_run_awards(guild: discord.Guild) -> None:
    """Раз в месяц (1-го числа или при первом запуске после него) подводит итоги прошлого месяца."""
    month_end = dt.datetime.now(TZ).replace(day=1, hour=0, minute=0, second=0, microsecond=0)
    month_start = add_months(month_end, -1)
    period = f"{month_start.year}-{month_start.month:02d}"
    claimed = await bot.pool.fetchval(
        "INSERT INTO award_runs (guild_id, period) VALUES ($1, $2) ON CONFLICT DO NOTHING RETURNING period",
        guild.id, period,
    )
    first_link = await bot.pool.fetchval(
        "SELECT min(linked_at) FROM (SELECT linked_at FROM dota_links WHERE guild_id = $1 "
        "UNION ALL SELECT linked_at FROM pubg_links WHERE guild_id = $1) AS l",
        guild.id,
    )
    if claimed is None or first_link is None or month_end <= first_link:
        return  # уже подводили, или месяц целиком до начала учёта матчей
    parts = [f"**Итоги за {MONTHS[month_start.month - 1]} {month_start.year}**",
             await award_text(guild.id, month_start, month_end, "ваучер Steam 10 $")]
    if month_end.month in (1, 4, 7, 10):
        quarter_start = add_months(month_end, -3)
        parts += ["", f"**Итоги квартала ({MONTHS[quarter_start.month - 1]} — {MONTHS[month_start.month - 1]})**",
                  await award_text(guild.id, quarter_start, month_end, "грамота и медаль 🏅")]
    marginals = [r["user_id"] for r in await bot.pool.fetch(
        "SELECT user_id FROM marginals WHERE guild_id = $1", guild.id
    )]
    if marginals:
        parts += ["", "Маргиналы пропустили эту выдачу и снова участвуют: "
                  + ", ".join(f"<@{u}>" for u in marginals)]
    await bot.pool.execute("DELETE FROM marginals WHERE guild_id = $1", guild.id)
    await sync_roles(guild)

    channel = bot.get_channel(int(CIVIC_CHANNEL_ID)) if CIVIC_CHANNEL_ID else None
    if channel is not None and channel.guild.id == guild.id:
        embed = discord.Embed(title="🏆 Награды", description="\n".join(parts), color=discord.Color.gold())
        embed.set_footer(text="Награды выдаёт админ")
        await channel.send(embed=embed)


@tasks.loop(hours=1)
async def civic_loop() -> None:
    for guild in bot.guilds:
        try:
            await maybe_run_awards(guild)
            await sync_rank_roles(guild)
        except Exception as e:
            print(f"Ошибка ролей/наград на {guild.id}: {e!r}")


@civic_loop.before_loop
async def before_civic_loop() -> None:
    await bot.wait_until_ready()


if __name__ == "__main__":
    if not TOKEN:
        raise SystemExit("Не задан DISCORD_TOKEN (см. .env.example)")
    if not DATABASE_URL:
        raise SystemExit("Не задан DATABASE_URL (см. .env.example)")
    bot.run(TOKEN)
