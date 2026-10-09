package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

var (
	applyTemplateID = pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	applyProviderID = pgtype.UUID{Bytes: [16]byte{9}, Valid: true}
)

type applyTemplateQueries struct {
	dbstore.Queries
	domain  string
	driver  string
	catalog []sqlc.TemplateProviderTemplateModel
	// existing model IDs make CreateModel fail with a unique violation.
	existing map[string]bool
	created  *[]sqlc.CreateModelParams
}

func (q applyTemplateQueries) GetProviderTemplateByID(context.Context, pgtype.UUID) (sqlc.TemplateProviderTemplate, error) {
	return sqlc.TemplateProviderTemplate{
		ID: applyTemplateID, Key: "example", Domain: q.domain, Name: "Example", Driver: q.driver,
		DefaultConfig: []byte(`{}`), Metadata: []byte(`{}`), Active: true,
	}, nil
}

func (applyTemplateQueries) CreateProviderFromTemplate(_ context.Context, arg sqlc.CreateProviderFromTemplateParams) (sqlc.Provider, error) {
	return sqlc.Provider{
		ID: applyProviderID, ProviderTemplateID: arg.ProviderTemplateID, Name: arg.Name,
		ClientType: arg.ClientType, Enable: arg.Enable, Config: arg.Config, Metadata: arg.Metadata,
	}, nil
}

func (q applyTemplateQueries) ListProviderTemplateModels(context.Context, pgtype.UUID) ([]sqlc.TemplateProviderTemplateModel, error) {
	return q.catalog, nil
}

func (q applyTemplateQueries) CreateModel(_ context.Context, arg sqlc.CreateModelParams) (sqlc.Model, error) {
	if q.existing[arg.ModelID] {
		return sqlc.Model{}, &pgconn.PgError{Code: "23505"}
	}
	*q.created = append(*q.created, arg)
	return sqlc.Model{ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, ModelID: arg.ModelID}, nil
}

func serveCreateFromTemplate(t *testing.T, q applyTemplateQueries, body string) *httptest.ResponseRecorder {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	h := NewProvidersHandler(logger, providers.NewService(logger, q, ""), models.NewService(logger, q))
	e := echo.New()
	h.Register(e)
	req := httptest.NewRequest(http.MethodPost, "/providers/from-template", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func templateCatalogModel(modelID, name, modelType, config string) sqlc.TemplateProviderTemplateModel {
	return sqlc.TemplateProviderTemplateModel{
		ProviderTemplateID: applyTemplateID, ModelID: modelID, Name: name, Type: modelType,
		Config: []byte(config), Metadata: []byte(`{}`), Active: true,
	}
}

func TestCreateFromTemplateAppliesTemplateModelsDisabled(t *testing.T) {
	var created []sqlc.CreateModelParams
	q := applyTemplateQueries{
		domain: "llm",
		driver: string(models.ClientTypeOpenAICompletions),
		catalog: []sqlc.TemplateProviderTemplateModel{
			templateCatalogModel("chat-a", "Chat A", "chat", `{"compatibilities":["tool-call","vision"],"context_window":128000}`),
			templateCatalogModel("embed-a", "", "embedding", `{"dimensions":1536}`),
			templateCatalogModel("already-there", "Existing", "chat", `{}`),
			templateCatalogModel("image-a", "Image A", "image", `{}`),
		},
		existing: map[string]bool{"already-there": true},
		created:  &created,
	}
	rec := serveCreateFromTemplate(t, q, `{"template_id":"07000000-0000-0000-0000-000000000000","apply_template_models":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(created) != 2 {
		t.Fatalf("created %d models, want chat and embedding only: %+v", len(created), created)
	}
	// The list API orders newest first; back-to-front creation keeps the
	// template order on screen.
	if created[0].ModelID != "embed-a" || created[1].ModelID != "chat-a" {
		t.Fatalf("creation order = %s, %s; want the catalog reversed", created[0].ModelID, created[1].ModelID)
	}
	byID := map[string]sqlc.CreateModelParams{}
	for _, params := range created {
		if params.Enable {
			t.Fatalf("model %s created enabled, want disabled like a bulk import", params.ModelID)
		}
		if params.ProviderID != applyProviderID {
			t.Fatalf("model %s provider = %v, want the new provider", params.ModelID, params.ProviderID)
		}
		byID[params.ModelID] = params
	}
	chat, ok := byID["chat-a"]
	if !ok || chat.Type != "chat" || chat.Name.String != "Chat A" {
		t.Fatalf("chat model = %+v", chat)
	}
	var chatConfig models.ModelConfig
	if err := json.Unmarshal(chat.Config, &chatConfig); err != nil {
		t.Fatal(err)
	}
	if chatConfig.ContextWindow == nil || *chatConfig.ContextWindow != 128000 || len(chatConfig.Compatibilities) != 2 {
		t.Fatalf("chat config = %+v, want template capabilities", chatConfig)
	}
	embed, ok := byID["embed-a"]
	if !ok || embed.Type != "embedding" || embed.Name.String != "embed-a" {
		t.Fatalf("embedding model = %+v, want model ID as fallback name", embed)
	}
}

func TestCreateFromTemplateLeavesModelsAloneByDefault(t *testing.T) {
	var created []sqlc.CreateModelParams
	q := applyTemplateQueries{
		domain:  "llm",
		driver:  string(models.ClientTypeOpenAICompletions),
		catalog: []sqlc.TemplateProviderTemplateModel{templateCatalogModel("chat-a", "Chat A", "chat", `{}`)},
		created: &created,
	}
	rec := serveCreateFromTemplate(t, q, `{"template_id":"07000000-0000-0000-0000-000000000000"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(created) != 0 {
		t.Fatalf("created %d models without apply_template_models, want none", len(created))
	}
}

func TestCreateFromTemplateSkipsNonLLMTemplates(t *testing.T) {
	var created []sqlc.CreateModelParams
	q := applyTemplateQueries{
		domain:  "speech",
		driver:  "openai-speech",
		catalog: []sqlc.TemplateProviderTemplateModel{templateCatalogModel("tts-1", "TTS", "chat", `{}`)},
		created: &created,
	}
	rec := serveCreateFromTemplate(t, q, `{"template_id":"07000000-0000-0000-0000-000000000000","apply_template_models":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	if len(created) != 0 {
		t.Fatalf("created %d models for a speech template, want none", len(created))
	}
}
