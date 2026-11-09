package main

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
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
-- откуда взялись отжимания: manual (/owe, /done), dota, pubg, court
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

CREATE TABLE IF NOT EXISTS dota_matches (          -- уже обработанные матчи
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

CREATE TABLE IF NOT EXISTS casino_bets (
    id         TEXT          PRIMARY KEY,
    guild_id   BIGINT        NOT NULL,
    user_id    BIGINT        NOT NULL,
    game       TEXT          NOT NULL,
    stake      INTEGER       NOT NULL,
    multiplier NUMERIC(8, 2) NOT NULL,
    payout     INTEGER       NOT NULL,
    result     JSONB         NOT NULL,
    created_at TIMESTAMPTZ   NOT NULL DEFAULT now()
);
`

// querier — общее у пула и транзакции (в тестах всё идёт внутри откатываемой транзакции).
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

var db querier

const dbTimeout = 10 * time.Second // зависший запрос не должен вешать команду

func dbCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), dbTimeout)
}

func connectDB(url string) (*pgxpool.Pool, error) {
	conf, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	conf.MaxConns = 4
	conf.MinConns = 1
	conf.MaxConnIdleTime = 5 * time.Minute
	conf.MaxConnLifetime = 30 * time.Minute
	conf.HealthCheckPeriod = time.Minute
	conf.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeDescribeExec // без кэша: работаем через пулер Supabase
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, conf)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

func exec(sql string, args ...any) error {
	ctx, cancel := dbCtx()
	defer cancel()
	_, err := db.Exec(ctx, sql, args...)
	return err
}

// fetch возвращает строки как структуры T (поля по порядку колонок).
func fetch[T any](sql string, args ...any) ([]T, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[T])
}

// column возвращает первую колонку всех строк.
func column[T any](sql string, args ...any) ([]T, error) {
	ctx, cancel := dbCtx()
	defer cancel()
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[T])
}

// fetchOne читает одну строку в dest; found=false, если строк нет.
func fetchOne(sql string, args []any, dest ...any) (found bool, err error) {
	ctx, cancel := dbCtx()
	defer cancel()
	err = db.QueryRow(ctx, sql, args...).Scan(dest...)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func addEntry(guildID, userID int64, kind string, amount int, authorID int64, reason *string, source string) error {
	return exec(`INSERT INTO entries (guild_id, user_id, kind, amount, author_id, reason, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, guildID, userID, kind, amount, authorID, reason, source)
}

type stat struct{ Owed, Done int }

func (s stat) remaining() int { return max(0, s.Owed-s.Done) }

// guildStats: user_id -> назначено и сделано за последние windowDays дней.
func guildStats(guildID int64) (map[int64]stat, error) {
	rows, err := fetch[struct {
		UserID     int64
		Owed, Done int
	}](`SELECT user_id,
		COALESCE(SUM(amount) FILTER (WHERE kind = 'owe'), 0)::int,
		COALESCE(SUM(amount) FILTER (WHERE kind = 'done'), 0)::int
		FROM entries WHERE guild_id = $1 AND created_at >= now() - make_interval(days => $2)
		GROUP BY user_id`, guildID, windowDays)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]stat, len(rows))
	for _, r := range rows {
		out[r.UserID] = stat{r.Owed, r.Done}
	}
	return out, nil
}

func userStats(guildID, userID int64) (stat, error) {
	stats, err := guildStats(guildID)
	return stats[userID], err
}

// ludomanBalance: user_id -> сколько отжиманий выиграно (+) или проиграно (−) на /done_ludoman.
func ludomanBalance(guildID int64) (map[int64]int, error) {
	rows, err := fetch[struct {
		UserID int64
		Delta  int
	}](`SELECT user_id, SUM(amount - substring(reason from 'лудомания: ([0-9]+) ×')::int)::int
		FROM entries WHERE guild_id = $1 AND kind = 'done' AND reason LIKE '🎰 лудомания:%'
		AND created_at >= now() - make_interval(days => $2) GROUP BY user_id`, guildID, windowDays)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(rows))
	for _, r := range rows {
		out[r.UserID] = r.Delta
	}
	return out, nil
}
