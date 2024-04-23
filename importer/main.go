package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	// "github.com/GitTsubasa/gumby/opencc"
	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/custom"
	"github.com/blevesearch/bleve/v2/analysis/lang/en"
	"github.com/blevesearch/bleve/v2/analysis/token/lowercase"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/single"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/unicode"
	"github.com/blevesearch/bleve/v2/analysis/tokenizer/whitespace"
	"github.com/blevesearch/bleve/v2/mapping"
)

var (
	indexPath     = flag.String("index_path", "dict.bleve", "Path to index.")
	inputPath     = flag.String("input_path", "dictionaries", "Path to input.")
	t2sPath       = flag.String("t2s_path", "/usr/share/opencc/t2s.json", "Path to t2s.json")
	writeToStdout = flag.Bool("write_to_stdout", false, "Write augmented entries to stdout?")
)

var diacriticsReplacer = strings.NewReplacer(
	"á", "aa",
	"ó", "o",
	"ú", "oo",
	"ü", "ui",
	"û", "u",
	"ö", "oe",
	"'", "h",
)

func buildIndexMapping() (mapping.IndexMapping, error) {
	indexMapping := bleve.NewIndexMapping()

	if err := indexMapping.AddCustomAnalyzer("unicode_tokenize",
		map[string]interface{}{
			"type":          custom.Name,
			"char_filters":  []interface{}{},
			"tokenizer":     unicode.Name,
			"token_filters": []interface{}{},
		}); err != nil {
		return nil, err
	}

	if err := indexMapping.AddCustomAnalyzer("whitespace_tokenize",
		map[string]interface{}{
			"type":          custom.Name,
			"char_filters":  []interface{}{},
			"tokenizer":     whitespace.Name,
			"token_filters": []interface{}{},
		}); err != nil {
		return nil, err
	}

	if err := indexMapping.AddCustomAnalyzer("single_tokenize",
		map[string]interface{}{
			"type":          custom.Name,
			"char_filters":  []interface{}{},
			"tokenizer":     single.Name,
			"token_filters": []interface{}{},
		}); err != nil {
		return nil, err
	}

	if err := indexMapping.AddCustomAnalyzer("en_nostop",
		map[string]interface{}{
			"type":         custom.Name,
			"char_filters": []interface{}{},
			"tokenizer":    unicode.Name,
			"token_filters": []interface{}{
				en.PossessiveName,
				lowercase.Name,
				en.SnowballStemmerName,
			},
		}); err != nil {
		return nil, err
	}

	entryDocumentMapping := bleve.NewDocumentMapping()
	{
		wordFieldMapping := bleve.NewTextFieldMapping()
		wordFieldMapping.Analyzer = "unicode_tokenize"
		entryDocumentMapping.AddFieldMappingsAt("word", wordFieldMapping)

		simplifiedMapping := bleve.NewTextFieldMapping()
		simplifiedMapping.Analyzer = "unicode_tokenize"
		simplifiedMapping.IncludeInAll = false
		entryDocumentMapping.AddFieldMappingsAt("simplified", simplifiedMapping)

		source := bleve.NewTextFieldMapping()
		source.Analyzer = "single_tokenize"
		source.IncludeInAll = false
		entryDocumentMapping.AddFieldMappingsAt("source", source)

		definitionDocumentMapping := bleve.NewDocumentMapping()
		{
			meaningsMapping := bleve.NewTextFieldMapping()
			meaningsMapping.Analyzer = "en_nostop"
			definitionDocumentMapping.AddFieldMappingsAt("meanings", meaningsMapping)

			readingsMapping := bleve.NewTextFieldMapping()
			readingsMapping.Analyzer = "whitespace_tokenize"
			definitionDocumentMapping.AddFieldMappingsAt("readings", readingsMapping)

			readingsNoDiacritics := bleve.NewTextFieldMapping()
			readingsNoDiacritics.IncludeInAll = false
			readingsNoDiacritics.Analyzer = "whitespace_tokenize"
			definitionDocumentMapping.AddFieldMappingsAt("readings_no_diacritics", readingsNoDiacritics)
		}
		entryDocumentMapping.AddSubDocumentMapping("definitions", definitionDocumentMapping)
	}
	indexMapping.AddDocumentMapping("entry", entryDocumentMapping)

	return indexMapping, nil
}

const batchSize = 10000

// var t2s *opencc.Converter

func augmentEntry(doc map[string]interface{}) error {
	// word := doc["word"].(string)
	// simplified, err := t2s.Convert(word)
	// if err != nil {
	// 	return err
	// }

	// doc["simplified"] = []string{simplified}

	definitions := doc["definitions"].([]interface{})
	for _, def := range definitions {
		def := def.(map[string]interface{})
		readings := def["readings"].([]interface{})

		readingsNoDiacritics := make([]string, len(readings))
		for i, reading := range readings {
			readingsNoDiacritics[i] = diacriticsReplacer.Replace(reading.(string))
		}
		def["readings_no_diacritics"] = readingsNoDiacritics
	}

	return nil
}

func importFile(idx bleve.Index, path string) (int, error) {
	stdoutEncoder := json.NewEncoder(os.Stdout)
	source := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	input, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer input.Close()

	batch := idx.NewBatch()

	dec := json.NewDecoder(input)
	i := 0
	for {
		i++
		var doc map[string]interface{}

		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}

		if err != nil {
			return i, fmt.Errorf("failed to process entry %d: %w", i, err)
		}

		if err := augmentEntry(doc); err != nil {
			return i, fmt.Errorf("failed to augment entry %d: %w", i, err)
		}

		doc["source"] = source

		if *writeToStdout {
			stdoutEncoder.Encode(doc)
		}

		doc["_type"] = "entry"

		if err := batch.Index(doc["source"].(string)+":"+doc["word"].(string), doc); err != nil {
			return i, fmt.Errorf("failed to index entry %d: %w", i, err)
		}

		if i%batchSize == 0 {
			if err := idx.Batch(batch); err != nil {
				return i, err
			}

			log.Printf("Indexed %d entries from %s.", i, path)

			batch = idx.NewBatch()
		}
	}

	if err := idx.Batch(batch); err != nil {
		return i, err
	}

	return i, nil
}

func main() {
	flag.Parse()

	var err error
	// t2s, err = opencc.New(*t2sPath)
	// if err != nil {
	// 	log.Fatalf("Failed to initialize opencc: %s", err)
	// }

	mapping, err := buildIndexMapping()
	if err != nil {
		log.Fatalf("Failed to build index mapping: %s", err)
	}

	os.RemoveAll(*indexPath)
	idx, err := bleve.New(*indexPath, mapping)
	if err != nil {
		log.Fatalf("Failed to open index: %s", err)
	}

	inputs, err := os.ReadDir(*inputPath)
	if err != nil {
		log.Fatalf("Failed to list inputs: %s", err)
	}

	for _, fi := range inputs {
		path := filepath.Join(*inputPath, fi.Name())

		if filepath.Ext(path) != ".ndjson" {
			continue
		}

		log.Printf("Indexing file %s", path)
		n, err := importFile(idx, path)
		if err != nil {
			log.Fatalf("Failed to process file %s: %s", path, err)
		}
		log.Printf("Indexed %d entries from %s", n, fi.Name())
	}
}
