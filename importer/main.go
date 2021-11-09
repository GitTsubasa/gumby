package main

import (
	"encoding/json"
	"flag"
	"io"
	"log"
	"os"

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
	indexPath = flag.String("index_path", "dict.bleve", "Path to index.")
	inputPath = flag.String("input_path", "dict.ndjson", "Path to input.")
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

func main() {
	flag.Parse()

	mapping, err := buildIndexMapping()
	if err != nil {
		log.Fatalf("Failed to build index mapping: %s", err)
	}

	os.RemoveAll(*indexPath)
	idx, err := bleve.New(*indexPath, mapping)
	if err != nil {
		log.Fatalf("Failed to open index: %s", err)
	}

	input, err := os.Open(*inputPath)
	if err != nil {
		log.Fatalf("Failed to open input: %s", err)
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
			log.Fatalf("Failed to process entry %d: %s", i, err)
		}

		doc["_type"] = "entry"

		if err := batch.Index(doc["source"].(string)+":"+doc["word"].(string), doc); err != nil {
			log.Fatalf("Failed to index index entry %d: %s", i, err)
		}

		if i%batchSize == 0 {
			if err := idx.Batch(batch); err != nil {
				log.Fatalf("Failed to run batch: %s", err)
			}

			log.Printf("Indexed %d entries.", i)

			batch = idx.NewBatch()
		}
	}

	if err := idx.Batch(batch); err != nil {
		log.Fatalf("Failed to run batch: %s", err)
	}

	log.Printf("Indexed %d entries.", i)
}
