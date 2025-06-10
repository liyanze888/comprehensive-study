package codes

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/typesense/typesense-go/typesense"
	"github.com/typesense/typesense-go/typesense/api"
	"github.com/typesense/typesense-go/typesense/api/pointer"
	"io"
	"log/slog"
	"strings"
)

type (
	TypeSenseWorker struct {
		clients *typesense.Client
	}
)

func (t *TypeSenseWorker) DeleteIndex(indexName string) {
	response, err := t.clients.Collection(indexName).Delete(context.Background())
	if err != nil {
		var tsErr *typesense.HTTPError
		ok := errors.As(err, &tsErr)
		if !ok {
			slog.Error("delete index error", err, slog.Any("collection name", indexName))
			return
		}
		var result map[string]interface{}
		err = json.Unmarshal(tsErr.Body, &result)
		if err != nil {
			slog.Error("delete index error", err, slog.Any("collection name", indexName), slog.Any("resp", response))
		}
		return
	}
	return
}

func (t *TypeSenseWorker) CreateIndex(schema *api.CollectionSchema) {
	response, err := t.clients.Collections().Create(context.Background(), schema)
	if err != nil {
		var tsErr *typesense.HTTPError
		ok := errors.As(err, &tsErr)
		if !ok {
			slog.Error("Create index error", err, slog.Any("schema", schema))
			return
		}
		var result map[string]interface{}
		err = json.Unmarshal(tsErr.Body, &result)
		if err != nil {
			slog.Error("Create index error", err, slog.Any("schema", schema), slog.Any("resp", response))
		}
		return
	}
	slog.Info("CreateIndex", slog.Any("schema", schema), slog.Any("resp", response))
}

func (t *TypeSenseWorker) UpdateData(indexName, content string) {
	result, err := t.clients.Collection(indexName).Documents().ImportJsonl(context.Background(), strings.NewReader(content), &api.ImportDocumentsParams{
		Action: pointer.String("upsert"),
	})
	if err != nil {
		var tsErr *typesense.HTTPError
		ok := errors.As(err, &tsErr)
		if !ok {
			slog.Error("UpdateData error", err, slog.Any("indexName", indexName), slog.Any("content", content))
			return
		}
		var tmpResult map[string]interface{}
		err = json.Unmarshal(tsErr.Body, &tmpResult)
		if err != nil {
			slog.Error("UpdateData error", err, slog.Any("indexName", indexName), slog.Any("content", content))
			return
		}
		slog.Error("UpdateData error", err, slog.Any("indexName", indexName), slog.Any("content", content), slog.Any("result", result))
		return
	}
	all, err := io.ReadAll(result)
	slog.Error("UpdateData error", err, slog.Any("indexName", indexName), slog.Any("content", content), slog.Any("all", string(all)))
}

func (t *TypeSenseWorker) Search(content, indexName string) {
	var params api.MultiSearchCollectionParameters
	err := json.Unmarshal([]byte(content), &params)
	if err != nil {
		slog.Error("SearchData error", err, slog.Any("content", content))
		return
	}
	// 缓存60s
	multiParams := &api.MultiSearchParams{}
	params.Collection = indexName

	perform, err := t.clients.MultiSearch.Perform(context.Background(), multiParams, api.MultiSearchSearchesParameter{
		Searches: []api.MultiSearchCollectionParameters{
			params,
		},
	})
	if err != nil {
		var tsErr *typesense.HTTPError
		ok := errors.As(err, &tsErr)
		if !ok {
			slog.Error("SearchData error", err, slog.Any("content", content))
			return
		}
		var result map[string]interface{}
		err = json.Unmarshal(tsErr.Body, &result)
		if err != nil {
			slog.Error("SearchData error", err, slog.Any("content", content))
			return
		}
		slog.Error("SearchData error", err, slog.Any("content", content), result)
		return
	}
	for _, r := range perform.Results {
		if r.Hits != nil {
			for _, h := range *r.Hits {
				slog.Info("SearchData", slog.Any("result", h.Document))
			}
		}
	}
}

func NewTypeSenseWorker(tsUrl, tsKey string) *TypeSenseWorker {
	tsClient := typesense.NewClient(
		typesense.WithServer(tsUrl),
		typesense.WithAPIKey(tsKey),
	)
	return &TypeSenseWorker{
		clients: tsClient,
	}
}
