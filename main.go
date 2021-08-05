package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"

	"github.com/bwmarrin/discordgo"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/kelseyhightower/envconfig"
)

var (
	createCommands = flag.Bool("create_commands", false, "Create commands?")
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

func (b *bot) findDefinitions(ctx context.Context, word string) ([]definition, error) {
	var definitions []definition

	rows, err := b.db.Query(ctx, `
		select
			readings, array_agg(meanings.meaning order by meanings.id)
		from
			definitions, meanings
		where
			word = $1 and
			meanings.definition_id = definitions.id
		group by
			readings
	`, word)
	if err != nil {
		return nil, err
	}

	for rows.Next() {
		var def definition
		if err := rows.Scan(&def.readings, &def.meanings); err != nil {
			return nil, err
		}

		definitions = append(definitions, def)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return definitions, nil
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

		meanings, err := b.findMeanings(ctx, words)
		if err != nil {
			log.Printf("Failed to find meanings: %s", err)
			return
		}

		searchOutput, err := makeSearchOutput(payload.Query, words, meanings, payload.Page, hasNext)
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

		definitions, err := b.findDefinitions(ctx, word)
		if err != nil {
			log.Printf("Failed to get definitions: %s", err)
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
		order by
			ts_rank_cd(meaning_index_col, plainto_tsquery('english', $1), 8) desc
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

func (b *bot) findMeanings(ctx context.Context, words []string) (map[string]string, error) {
	definitions := make(map[string]string, len(words))

	rows, err := b.db.Query(ctx, `
		select
			definitions.word, string_agg(meanings.meaning, '; ' order by meanings.id)
		from
			definitions, meanings
		where
			definitions.word = any($1) and
			meanings.definition_id = definitions.id
		group by
			definitions.word
	`, words)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var word string
		var meanings string

		if err := rows.Scan(&word, &meanings); err != nil {
			return nil, err
		}

		definitions[word] = meanings
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return definitions, nil
}

func makeSearchOutput(query string, words []string, meanings map[string]string, page int, hasNext bool) (*discordgo.WebhookEdit, error) {
	var selectMenuOptions []discordgo.SelectMenuOption
	for _, word := range words {
		selectMenuOptions = append(selectMenuOptions, discordgo.SelectMenuOption{
			Label:       word,
			Description: meanings[word],
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

func makeEntryOutput(word string, definitions []definition) *discordgo.MessageEmbed {
	prettyDefs := make([]string, len(definitions))
	for i, def := range definitions {
		prettyDefs[i] = fmt.Sprintf("_%s_\n%s", strings.Join(def.readings, ", "), strings.Join(def.meanings, "; "))
	}

	return &discordgo.MessageEmbed{
		Title:       word,
		Description: strings.Join(prettyDefs, "\n\n"),
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
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0x4B5563,
						Title:       fmt.Sprintf("Search results for “%s”", query),
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

	meanings, err := b.findMeanings(ctx, words)
	if err != nil {
		log.Printf("Failed to find meanings: %s", err)
		return
	}

	searchOutput, err := makeSearchOutput(query, words, meanings, 0, hasNext)
	if err != nil {
		log.Printf("Failed to make search output: %s", err)
		return
	}

	var embeds []*discordgo.MessageEmbed
	if len(words) == 1 {
		word := words[0]
		definitions, err := b.findDefinitions(ctx, word)
		if err != nil {
			log.Printf("Failed to get definitions: %s", err)
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
	flag.Parse()

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

	if *createCommands {
		for _, cmd := range commands {
			if _, err := discord.ApplicationCommandCreate(discord.State.User.ID, "", cmd); err != nil {
				log.Fatalf("Unable to create command %s: %v\n", cmd.Name, err)
			}

			log.Printf("Created command: %s", cmd.Name)
		}
	}

	bot := bot{db: db, discord: discord}
	discord.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		bot.handleInteraction(context.Background(), i)
	})

	stop := make(chan os.Signal)
	signal.Notify(stop, os.Interrupt)
	<-stop
}
