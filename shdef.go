package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/search/query"
	index "github.com/blevesearch/bleve_index_api"
	"github.com/bwmarrin/discordgo"

	_ "github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	_ "github.com/blevesearch/bleve/v2/analysis/lang/en"
	_ "github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	_ "github.com/blevesearch/bleve/v2/analysis/tokenizer/single"
	_ "github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	_ "github.com/blevesearch/bleve/v2/analysis/tokenizer/whitespace"
)

type shdefActionGoToPage struct {
	Query  string `json:"query"`
	Source string `json:"source"`
	Page   int    `json:"page"`
}

const (
	customIDPrefixShdefGoToPage string = "shdef:goToPage"
	customIDPrefixShdefSelect   string = "shdef:select"
)

type entry struct {
	word        string
	source      string
	simplified  []string
	definitions []definition
}

type definition struct {
	readings []string
	meanings []string
}

func (b *bot) findEntries(ctx context.Context, ids []string) (map[string]entry, error) {
	entries := make(map[string]entry)
	for _, id := range ids {
		doc, err := b.index.Document(id)
		if err != nil {
			return nil, err
		}

		var e entry
		doc.VisitFields(func(f index.Field) {
			arrayPositions := f.ArrayPositions()

			switch f.Name() {
			case "word":
				e.word = string(f.Value())
			case "simplified":
				e.simplified = append(e.simplified, string(f.Value()))
			case "definitions.meanings":
				for len(e.definitions) <= int(arrayPositions[0]) {
					e.definitions = append(e.definitions, definition{})
				}

				e.definitions[int(arrayPositions[0])].meanings = append(e.definitions[int(arrayPositions[0])].meanings, string(f.Value()))
			case "definitions.readings":
				for len(e.definitions) <= int(arrayPositions[0]) {
					e.definitions = append(e.definitions, definition{})
				}

				e.definitions[int(arrayPositions[0])].readings = append(e.definitions[int(arrayPositions[0])].readings, string(f.Value()))
			case "definitions.readings_no_diacritics":
				for len(e.definitions) <= int(arrayPositions[0]) {
					e.definitions = append(e.definitions, definition{})
				}
			case "source":
				e.source = string(f.Value())
			}
		})

		entries[id] = e
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

		results, count, err := b.lookup(ctx, payload.Query, payload.Source, queryLimit+1, payload.Page*queryLimit)
		if err != nil {
			log.Printf("Failed to find words: %s", err)
			return
		}

		hasNext := false
		if len(results) > queryLimit {
			results = results[:queryLimit]
			hasNext = true
		}

		resultIDs := make([]string, len(results))
		for i, r := range results {
			resultIDs[i] = r.id
		}

		entries, err := b.findEntries(ctx, resultIDs)
		if err != nil {
			log.Printf("Failed to find entries: %s", err)
			return
		}

		searchOutput, err := makeSearchOutput(payload.Query, payload.Source, count, resultIDs, entries, payload.Page, hasNext)
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

		entry, ok := entries[word]
		if !ok {
			log.Printf("Failed to get entry")
			return
		}

		if err := b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{Type: discordgo.InteractionResponseDeferredMessageUpdate}); err != nil {
			log.Printf("Failed to respond: %s", err)
			return
		}

		if _, err := b.discord.InteractionResponseEdit(b.discord.State.User.ID, i.Interaction, &discordgo.WebhookEdit{
			Embeds: []*discordgo.MessageEmbed{makeEntryOutput(entry)},
		}); err != nil {
			log.Printf("Failed to edit response: %s", err)
			return
		}
	}
}

type result struct {
	id                   string
	word                 string
	simplified           []string
	readings             []string
	readingsNoDiacritics []string
	source               string
}

func isExactMatch(r result, q string) bool {
	if q == r.word {
		return true
	}

	for _, s := range r.simplified {
		if q == s {
			return true
		}
	}

	for _, rd := range r.readings {
		if q == rd {
			return true
		}
	}

	for _, rd := range r.readingsNoDiacritics {
		if q == rd {
			return true
		}
	}

	return false
}

func fieldToStringList(v interface{}) []string {
	single, ok := v.(string)
	if ok {
		return []string{single}
	}

	fields := v.([]interface{})
	out := make([]string, len(fields))

	for i, f := range fields {
		out[i] = f.(string)
	}

	return out
}

func (b *bot) lookup(ctx context.Context, q string, source string, limit int, offset int) ([]result, uint64, error) {
	q = strings.TrimSpace(q)

	meaningMatch := bleve.NewMatchPhraseQuery(q)
	meaningMatch.SetField("definitions.meanings")

	readingsMatch := bleve.NewMatchPhraseQuery(q)
	readingsMatch.SetField("definitions.readings")

	readingsNoDiacriticsMatch := bleve.NewMatchPhraseQuery(q)
	readingsNoDiacriticsMatch.SetField("definitions.readings_no_diacritics")

	wordMatch := bleve.NewMatchPhraseQuery(q)
	wordMatch.SetField("word")

	simplifiedMatch := bleve.NewMatchPhraseQuery(q)
	simplifiedMatch.SetField("simplified")

	var sourceMatch query.Query = bleve.NewMatchAllQuery()
	if source != "" {
		realSourceMatch := bleve.NewTermQuery(source)
		realSourceMatch.SetField("source")
		sourceMatch = realSourceMatch
	}

	req := bleve.NewSearchRequest(bleve.NewConjunctionQuery(bleve.NewDisjunctionQuery(meaningMatch, readingsMatch, readingsNoDiacriticsMatch, wordMatch, simplifiedMatch), sourceMatch))
	req.Size = limit
	req.From = offset
	req.Fields = []string{"word", "simplified", "definitions.readings", "definitions.readings_no_diacritics", "source"}

	r, err := b.index.Search(req)
	if err != nil {
		return nil, 0, err
	}

	results := make([]result, len(r.Hits))
	for i, hit := range r.Hits {
		results[i] = result{
			id:                   hit.ID,
			word:                 hit.Fields["word"].(string),
			simplified:           fieldToStringList(hit.Fields["simplified"]),
			readings:             fieldToStringList(hit.Fields["definitions.readings"]),
			readingsNoDiacritics: fieldToStringList(hit.Fields["definitions.readings_no_diacritics"]),
			source:               hit.Fields["source"].(string),
		}
	}

	return results, r.Total, nil
}

func truncate(s string, length int, ellipsis string) string {
	if len(s) <= length {
		return s
	}

	length -= len(ellipsis)
	var buf strings.Builder
	for _, r := range []rune(s) {
		next := string(r)
		if buf.Len()+len(next) > length {
			break
		}
		buf.WriteString(next)
	}

	return buf.String() + ellipsis
}

func makeSearchOutput(query string, source string, count uint64, ids []string, entries map[string]entry, page int, hasNext bool) (*discordgo.WebhookEdit, error) {
	var selectMenuOptions []discordgo.SelectMenuOption
	for _, id := range ids {
		entry := entries[id]

		var readings []string
		for _, definition := range entry.definitions {
			readings = append(readings, definition.readings...)
		}

		var meanings []string
		for _, definition := range entry.definitions {
			meanings = append(meanings, definition.meanings...)
		}

		selectMenuOptions = append(selectMenuOptions, discordgo.SelectMenuOption{
			Label:       fmt.Sprintf("%s (%s)", entry.word, strings.Join(readings, ", ")),
			Description: truncate(strings.Join(meanings, "; "), 100, "..."),
			Value:       id,
		})
	}

	var title string
	var components []discordgo.MessageComponent
	if count == 1 {
		title = fmt.Sprintf("1 result for “%s”", query)
	} else {
		title = fmt.Sprintf("%d results for “%s”", count, query)

		prevPagePayload, err := json.Marshal(shdefActionGoToPage{Query: query, Source: source, Page: page - 1})
		if err != nil {
			return nil, err
		}

		nextPagePayload, err := json.Marshal(shdefActionGoToPage{Query: query, Source: source, Page: page + 1})
		if err != nil {
			return nil, err
		}

		components = []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.SelectMenu{
						Placeholder: fmt.Sprintf("Select from results %d to %d", page*queryLimit+1, page*queryLimit+len(entries)),
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
		}
	}

	return &discordgo.WebhookEdit{
		Content:    fmt.Sprintf("**%s**", title),
		Components: components,
	}, nil
}

func makeEntryOutput(e entry) *discordgo.MessageEmbed {
	prettyDefs := make([]string, len(e.definitions))
	for i, def := range e.definitions {
		prettyMeaning := "_Meaning unknown_"
		if len(def.meanings) > 0 {
			prettyMeaning = strings.Join(def.meanings, "\n")
		}
		prettyDefs[i] = fmt.Sprintf("**%s**\n%s", strings.Join(def.readings, ", "), prettyMeaning)
	}

	var prettySimplifieds []string

	wordRunes := []rune(e.word)

	for _, s := range e.simplified {
		var sb strings.Builder
		simplifiedDiffers := false
		for i, sr := range []rune(s) {
			wr := wordRunes[i]

			if sr != wr {
				sb.WriteRune(sr)
				simplifiedDiffers = true
			} else {
				sb.WriteRune('〃')
			}
		}

		if simplifiedDiffers {
			prettySimplifieds = append(prettySimplifieds, sb.String())
		}
	}

	title := e.word
	if len(prettySimplifieds) > 0 {
		title = title + " (" + strings.Join(prettySimplifieds, ", ") + ")"
	}

	return &discordgo.MessageEmbed{
		Title:       title,
		Color:       0x005BAC,
		Description: fmt.Sprintf("%s\n\n_%s_", strings.Join(prettyDefs, "\n\n"), sources[e.source]),
		Footer:      defaultFooter,
	}
}

const queryLimit = 25

func (b *bot) handleShdef(ctx context.Context, i *discordgo.InteractionCreate, source string) {
	options := i.ApplicationCommandData().Options

	query := strings.TrimSpace(options[0].StringValue())

	if query == "" {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0xDC2626,
						Description: "You have to provide something to look up!",
						Footer:      defaultFooter,
					},
				},
			},
		})
		return
	}

	results, count, err := b.lookup(ctx, query, source, queryLimit+1, 0)
	if err != nil {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0xDC2626,
						Description: "An error occurred.",
						Footer:      defaultFooter,
					},
				},
			},
		})
		log.Printf("Failed to lookup word: %s", err)
		return
	}

	if count == 0 {
		b.discord.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Content: fmt.Sprintf("**0 results for “%s”**", query),
				Embeds: []*discordgo.MessageEmbed{
					{
						Color:       0x4B5563,
						Description: "No results found.",
						Footer:      defaultFooter,
					},
				},
			},
		})
		return
	}

	hasNext := false
	if len(results) > queryLimit {
		results = results[:queryLimit]
		hasNext = true
	}

	resultIDs := make([]string, len(results))
	for i, r := range results {
		resultIDs[i] = r.id
	}

	entries, err := b.findEntries(ctx, resultIDs)
	if err != nil {
		log.Printf("Failed to find meanings: %s", err)
		return
	}

	searchOutput, err := makeSearchOutput(query, source, count, resultIDs, entries, 0, hasNext)
	if err != nil {
		log.Printf("Failed to make search output: %s", err)
		return
	}

	var embeds []*discordgo.MessageEmbed
	if len(results) == 1 || (len(results) > 0 && isExactMatch(results[0], query) && !isExactMatch(results[1], query)) {
		entries, err := b.findEntries(ctx, resultIDs)
		if err != nil {
			log.Printf("Failed to get entries: %s", err)
			return
		}

		entry, ok := entries[resultIDs[0]]
		if !ok {
			log.Printf("Failed to get definitions")
			return
		}

		embeds = []*discordgo.MessageEmbed{makeEntryOutput(entry)}
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
