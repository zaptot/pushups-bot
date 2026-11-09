package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var (
	session   *discordgo.Session
	botUserID int64
)

const (
	colorOrange     = 0xe67e22
	colorGold       = 0xf1c40f
	colorBlurple    = 0x5865f2
	colorRed        = 0xe74c3c
	colorDarkOrange = 0xa84300
	colorBlue       = 0x3498db
	colorDarkGold   = 0xc27c0e
	colorPurple     = 0x9b59b6

	flagEphemeral = discordgo.MessageFlagsEphemeral
)

func parseID(s string) int64 {
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func idStr(v int64) string { return strconv.FormatInt(v, 10) }

func mention(v int64) string { return fmt.Sprintf("<@%d>", v) }

func mentions(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = mention(id)
	}
	return strings.Join(parts, ", ")
}

func truncate(s string, limit int) string {
	r := []rune(s)
	if len(r) > limit {
		return string(r[:limit])
	}
	return s
}

// request — входящая команда или нажатие кнопки.
type request struct {
	i       *discordgo.InteractionCreate
	options map[string]*discordgo.ApplicationCommandInteractionDataOption
}

func (r *request) guildID() int64 { return parseID(r.i.GuildID) }
func (r *request) inGuild() bool  { return r.i.GuildID != "" }

func (r *request) user() *discordgo.User {
	if r.i.Member != nil {
		return r.i.Member.User
	}
	return r.i.User
}

func (r *request) userID() int64       { return parseID(r.user().ID) }
func (r *request) userMention() string { return mention(r.userID()) }

func (r *request) respond(data *discordgo.InteractionResponseData, kind discordgo.InteractionResponseType) error {
	return session.InteractionRespond(r.i.Interaction, &discordgo.InteractionResponse{Type: kind, Data: data})
}

func (r *request) reply(content string) error {
	return r.respond(&discordgo.InteractionResponseData{Content: content}, discordgo.InteractionResponseChannelMessageWithSource)
}

func (r *request) replyHidden(content string) error {
	return r.respond(&discordgo.InteractionResponseData{Content: content, Flags: flagEphemeral}, discordgo.InteractionResponseChannelMessageWithSource)
}

func (r *request) replyEmbed(embed *discordgo.MessageEmbed, hidden bool) error {
	data := &discordgo.InteractionResponseData{Embeds: []*discordgo.MessageEmbed{embed}}
	if hidden {
		data.Flags = flagEphemeral
	}
	return r.respond(data, discordgo.InteractionResponseChannelMessageWithSource)
}

func (r *request) deferReply() error {
	return r.respond(nil, discordgo.InteractionResponseDeferredChannelMessageWithSource)
}

func (r *request) followup(content string, embeds []*discordgo.MessageEmbed, hidden bool) error {
	params := &discordgo.WebhookParams{Content: content, Embeds: embeds}
	if hidden {
		params.Flags = flagEphemeral
	}
	_, err := session.FollowupMessageCreate(r.i.Interaction, true, params)
	return err
}

func (r *request) noGuild() error {
	return r.replyHidden("Эта команда работает только на сервере.")
}

func (r *request) intOption(name string, fallback int) int {
	if o, ok := r.options[name]; ok {
		return int(o.IntValue())
	}
	return fallback
}

func (r *request) stringOption(name string) string {
	if o, ok := r.options[name]; ok {
		return o.StringValue()
	}
	return ""
}

// target — участник из параметра команды.
type target struct {
	ID       int64
	Bot      bool
	Name     string
	JoinedAt time.Time
}

func (t target) mention() string { return mention(t.ID) }

// userOption возвращает участника из параметра; без параметра — того, кто вызвал команду.
func (r *request) userOption(name string) target {
	o, ok := r.options[name]
	if !ok {
		out := target{ID: r.userID(), Bot: r.user().Bot, Name: r.user().DisplayName()}
		if r.i.Member != nil {
			out.Name = displayName(r.i.Member, r.user())
			out.JoinedAt = r.i.Member.JoinedAt
		}
		return out
	}
	id := o.Value.(string)
	resolved := r.i.ApplicationCommandData().Resolved
	out := target{ID: parseID(id), Name: id}
	var user *discordgo.User
	if resolved != nil {
		user = resolved.Users[id]
		if user != nil {
			out.Bot, out.Name = user.Bot, user.DisplayName()
		}
		if m := resolved.Members[id]; m != nil {
			out.Name = displayName(m, user)
			out.JoinedAt = m.JoinedAt
		}
	}
	return out
}

func displayName(m *discordgo.Member, u *discordgo.User) string {
	if m.Nick != "" {
		return m.Nick
	}
	if u != nil {
		return u.DisplayName()
	}
	return ""
}

func field(name, value string, inline bool) *discordgo.MessageEmbedField {
	return &discordgo.MessageEmbedField{Name: name, Value: value, Inline: inline}
}

func footer(text string) *discordgo.MessageEmbedFooter {
	return &discordgo.MessageEmbedFooter{Text: text}
}

func orEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// chunkEmbeds разбивает строки на embed'ы с лимитом Discord.
func chunkEmbeds(title string, color int, lines []string) []*discordgo.MessageEmbed {
	var out []*discordgo.MessageEmbed
	var b strings.Builder
	flush := func() {
		if b.Len() > 0 && len(out) < 10 {
			out = append(out, &discordgo.MessageEmbed{Title: title, Description: b.String(), Color: color})
		}
		b.Reset()
	}
	for _, line := range lines {
		if len([]rune(b.String()))+len([]rune(line))+1 > 4000 {
			flush()
		}
		b.WriteString(line + "\n")
	}
	flush()
	return out
}

func channelGuild(channelID string) (int64, bool) {
	ch, err := session.State.Channel(channelID)
	if err != nil {
		return 0, false
	}
	return parseID(ch.GuildID), true
}

func isRESTStatus(err error, status int) bool {
	var rest *discordgo.RESTError
	return errors.As(err, &rest) && rest.Response != nil && rest.Response.StatusCode == status
}

// protect не даёт панике в обработчике уронить весь бот.
func protect(name string) {
	if v := recover(); v != nil {
		log.Printf("Паника в %s: %v\n%s", name, v, debug.Stack())
	}
}

func withTimeout(d time.Duration) (discordgo.RequestOption, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	return discordgo.WithContext(ctx), cancel
}

func newHTTPClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }
