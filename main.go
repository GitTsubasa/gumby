package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/kelseyhightower/envconfig"
)

type config struct {
	DiscordToken string
	DatabaseURL  string
}

var (
	commands = []*discordgo.ApplicationCommand{
		{
			Name:        "shdef",
			Description: "Look up in dictionary.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "query",
					Description: "What to look up (by word or by meaning)",
					Required:    true,
				},
			},
		},
	}
)

type bot struct {
	db      *pgxpool.Pool
	discord *discordgo.Session
}

func (b *bot) handleInteraction(ctx context.Context, i *discordgo.InteractionCreate) {
	switch i.Type {
	case discordgo.InteractionApplicationCommand:
		switch i.ApplicationCommandData().Name {
		case "shdef":
			b.handleShdef(ctx, i)
		}
	case discordgo.InteractionMessageComponent:
		b.handleComponentInteraction(ctx, i)
	}
}

type shdefActionGoToPage struct {
	Query string `json:"query"`
	Page  int    `json:"page"`
}

const (
	customIDPrefixShdefGoToPage string = "shdef:goToPage"
	customIDPrefixShdefSelect   string = "shdef:select"
)

type definition struct {
	readings []string
	meanings []string
}

func (b *bot) findEntries(ctx context.Context, words []string) (map[string][]*definition, error) {
	entries := make(map[string][]*definition)

	definitionsByID := make(map[int64]*definition)

	// Get all readings first.
	if err := func() error {
		rows, err := b.db.Query(ctx, `
			select
				id, word, readings
			from
				definitions
			where
				word = any($1)
			order by id
		`, words)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var id int64
			var word string
			var readings []string

			if err := rows.Scan(&id, &word, &readings); err != nil {
				return err
			}

			definition := &definition{
				readings: readings,
			}
			entries[word] = append(entries[word], definition)

			definitionsByID[id] = definition
		}

		if err := rows.Err(); err != nil {
			return err
		}

		return nil
	}(); err != nil {
		return nil, err
	}

	definitionIDs := make([]int64, 0, len(definitionsByID))
	for id := range definitionsByID {
		definitionIDs = append(definitionIDs, id)
	}

	// Get all meanings now.
	if err := func() error {
		rows, err := b.db.Query(ctx, `
			select
				definition_id, meaning
			from
				meanings
			where
				definition_id = any($1)
			order by definition_id, id
		`, definitionIDs)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var definitionID int64
			var meaning string

			if err := rows.Scan(&definitionID, &meaning); err != nil {
				return err
			}

			definitionsByID[definitionID].meanings = append(definitionsByID[definitionID].meanings, meaning)
		}

		if err := rows.Err(); err != nil {
			return err
		}

		return nil
	}(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (b *bot) handleComponentInteraction(ctx context.Context, i *discordgo.InteractionCreate) {
	customID := i.Interaction.MessageComponentData().CustomID

	pipeIndex := strings.IndexRune(customID, '|')
	prefix := customID[:pipeIndex]
	rawPayload := []byte(customID[pipeIndex+1:])

	switch prefix {
	case customIDPrefixShdefGoToPage:
		var payload shdefActionGoToPage
		if err := json.Unmarshal(rawPayload, &payload); err != nil {
			log.Printf("Failed to unmarshal payload words: %s", err)
			return
		}

		words, err := b.lookup(ctx, payload.Query, queryLimit+1, payload.Page*queryLimit)
		if err != nil {
			log.Printf("Failed to find words: %s", err)
			return
		}

		hasNext := false
		if len(words) > queryLimit {
			words = words[:queryLimit]
			hasNext = true
		}

		entries, err := b.findEntries(ctx, words)
		if err != nil {
			log.Printf("Failed to find entries: %s", err)
			return
		}

		searchOutput, err := makeSearchOutput(payload.Query, words, entries, payload.Page, hasNext)
		if err != nil {
			log.Printf("Failed to make search output: %s", err)
			return
		}

		if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
			log.Printf("Failed to respond: %s", err)
			return
		}

		if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, i.Interaction, searchOutput); err != nil {
			log.Printf("Failed to edit response: %s", err)
			return
		}

	case customIDPrefixShdefSelect:
		word := i.Interaction.MessageComponentData().Values[0]

		entries, err := b.findEntries(ctx, []string{word})
		if err != nil {
			log.Printf("Failed to get entries: %s", err)
			return
		}

		definitions, ok := entries[word]
		if !ok {
			log.Printf("Failed to get definitions", err)
			return
		}

		if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
			log.Printf("Failed to respond: %s", err)
			return
		}

		if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
			Embeds: []*discordgo.MessageEmbed{makeEntryOutput(word, definitions)},
		}); err != nil {
			log.Printf("Failed to edit response: %s", err)
			return
		}
	}
}

func (b *bot) lookupByWord(ctx context.Context, query string, limit int, offset int) ([]string, error) {
	var words []string

	rows, err := b.db.Query(ctx, `
		select
			word
		from
			words
		where
			word like quote_like($1) || '%'
		order by
			word
		limit $2 offset $3
	`, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var word string
		if err := rows.Scan(&word); err != nil {
			return nil, err
		}

		words = append(words, word)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return words, nil
}

func (b *bot) lookupByMeaning(ctx context.Context, query string, limit int, offset int) ([]string, error) {
	var words []string

	rows, err := b.db.Query(ctx, `
		select
			word
		from
			meanings, definitions
		where
			meaning_index_col @@ plainto_tsquery('english', $1) and
			meanings.definition_id = definitions.id
		group by
			word
		order by
			max(ts_rank_cd(meaning_index_col, plainto_tsquery('english', $1), 8)) desc
		limit $2 offset $3
	`, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var word string
		if err := rows.Scan(&word); err != nil {
			return nil, err
		}

		words = append(words, word)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return words, nil
}

func (b *bot) lookup(ctx context.Context, query string, limit int, offset int) ([]string, error) {
	words, err := b.lookupByWord(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}

	if len(words) > 0 {
		return words, nil
	}

	return b.lookupByMeaning(ctx, query, limit, offset)
}

func makeSearchOutput(query string, words []string, entries map[string][]*definition, page int, hasNext bool) (*discordgo.WebhookEdit, error) {
	var selectMenuOptions []discordgo.SelectMenuOption
	for _, word := range words {
		var readings []string
		for _, definition := range entries[word] {
			readings = append(readings, definition.readings...)
		}

		var meanings []string
		for _, definition := range entries[word] {
			meanings = append(meanings, definition.meanings...)
		}

		selectMenuOptions = append(selectMenuOptions, discordgo.SelectMenuOption{
			Label:       fmt.Sprintf("%s (%s)", word, strings.Join(readings, ", ")),
			Description: strings.Join(meanings, ", "),
			Value:       word,
		})
	}

	prevPagePayload, err := json.Marshal(shdefActionGoToPage{Query: query, Page: page - 1})
	if err != nil {
		return nil, err
	}

	nextPagePayload, err := json.Marshal(shdefActionGoToPage{Query: query, Page: page + 1})
	if err != nil {
		return nil, err
	}

	return &discordgo.WebhookEdit{
		Content: fmt.Sprintf("**Results for “%s”**", query),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.SelectMenu{
						Placeholder: fmt.Sprintf("Select from results %d to %d", page*queryLimit+1, page*queryLimit+len(words)),
						Options:     selectMenuOptions,
						CustomID:    customIDPrefixShdefSelect + "|",
					},
				},
			},
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						Emoji:    discordgo.ComponentEmoji{Name: "◀️"},
						Label:    "Previous Page",
						Style:    discordgo.SecondaryButton,
						Disabled: page == 0,
						CustomID: customIDPrefixShdefGoToPage + "|" + string(prevPagePayload),
					},
					discordgo.Button{
						Emoji:    discordgo.ComponentEmoji{Name: "▶️"},
						Label:    "Next Page",
						Style:    discordgo.SecondaryButton,
						Disabled: !hasNext,
						CustomID: customIDPrefixShdefGoToPage + "|" + string(nextPagePayload),
					},
				},
			},
		},
	}, nil
}

func makeEntryOutput(word string, definitions []*definition) *discordgo.MessageEmbed {
	prettyDefs := make([]string, len(definitions))
	for i, def := range definitions {
		prettyDefs[i] = fmt.Sprintf("**%s**\n%s", strings.Join(def.readings, ", "), strings.Join(def.meanings, ", "))
	}

	return &discordgo.MessageEmbed{
		Title:       word,
		Color:       0x005BAC,
		Description: strings.Join(prettyDefs, "\n\n"),
		Footer: &discordgo.MessageEmbedFooter{
			Text: "www.shanghaivernacular.com",
		},
	}
}

const queryLimit = 25

func (b *bot) handleShdef(ctx context.Context, i *discordgo.InteractionCreate) {
	query := i.ApplicationCommandData().Options[0].StringValue()

	words, err := b.lookup(ctx, query, queryLimit+1, 0)

	if err != nil {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0xDC2626,
						Description: "An error occurred.",
					},
				},
			},
		})
		log.Printf("Failed to lookup word: %s", err)
		return
	}

	if len(words) == 0 {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("**Results for “%s”**", query),
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0x4B5563,
						Description: "No results found.",
					},
				},
			},
		})
		return
	}

	hasNext := false
	if len(words) > queryLimit {
		words = words[:queryLimit]
		hasNext = true
	}

	entries, err := b.findEntries(ctx, words)
	if err != nil {
		log.Printf("Failed to find meanings: %s", err)
		return
	}

	searchOutput, err := makeSearchOutput(query, words, entries, 0, hasNext)
	if err != nil {
		log.Printf("Failed to make search output: %s", err)
		return
	}

	var embeds []*discordgo.MessageEmbed
	if len(words) == 1 {
		word := words[0]

		entries, err := b.findEntries(ctx, []string{word})
		if err != nil {
			log.Printf("Failed to get entries: %s", err)
			return
		}

		definitions, ok := entries[word]
		if !ok {
			log.Printf("Failed to get definitions", err)
			return
		}

		embeds = []*discordgo.MessageEmbed{makeEntryOutput(word, definitions)}
	}

	if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Embeds:     embeds,
			Content:    searchOutput.Content,
			Components: searchOutput.Components,
		},
	}); err != nil {
		log.Printf("Failed to send interaction: %s", err)
		return
	}
}

func main() {
	var c config
	if err := envconfig.Process("gumby", &c); err != nil {
		log.Fatalf("Failed to parse envconfing: %s", err)
	}

	db, err := pgxpool.Connect(context.Background(), c.DatabaseURL)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}

	log.Printf("Connected to database.")

	discord, err := discordgo.New(c.DiscordToken)
	if err != nil {
		log.Fatalf("Unable to connect to Discord: %v\n", err)
	}

	discord.StateEnabled = false
	discord.Identify.Intents = discordgo.IntentsNone

	if err := discord.Open(); err != nil {
		log.Fatalf("Unable to connect to Discord: %v\n", err)
	}

	discord.UpdateGameStatus(0, "/shdef")

	log.Printf("Connected to Discord.")

	defer discord.Close()

	discord.AddHandler(func(d *discordgo.Session, g *discordgo.GuildCreate) {
		for _, cmd := range commands {
			if _, err := discord.ApplicationCommandCreate(discord.State.User.ID, g.Guild.ID, cmd); err != nil {
				log.Fatalf("Unable to create command %s: %v\n", cmd.Name, err)
			}

			log.Printf("Created command %s for %s", cmd.Name, g.Guild.ID)
		}
	})

	bot := bot{db: db, discord: discord}
	discord.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		bot.handleInteraction(context.Background(), i)
	})

	stop := make(chan os.Signal)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
