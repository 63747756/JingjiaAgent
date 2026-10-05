package repo

import (
	"context"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/google/uuid"

	"github.com/63747756/jingjiaagent/backend/consts"
	"github.com/63747756/jingjiaagent/backend/db"
	"github.com/63747756/jingjiaagent/backend/db/enttest"
	"github.com/63747756/jingjiaagent/backend/db/modelapikey"
)

func TestModelRepoGetDoesNotLoadRuntimeApikeys(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:model-repo-get-apikeys?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })

	userID := uuid.New()
	modelID := uuid.New()
	keyID := uuid.New()

	if _, err := client.User.Create().
		SetID(userID).
		SetName("user").
		SetRole(consts.UserRoleIndividual).
		SetStatus(consts.UserStatusActive).
		Save(ctx); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := client.Model.Create().
		SetID(modelID).
		SetUserID(userID).
		SetProvider("OpenAI").
		SetAPIKey("model-key").
		SetBaseURL("https://model.example/v1").
		SetModel("gpt-4.1").
		Save(ctx); err != nil {
		t.Fatalf("create model: %v", err)
	}
	if _, err := client.ModelApiKey.Create().
		SetID(keyID).
		SetUserID(userID).
		SetModelID(modelID).
		SetAPIKey("runtime-key").
		Save(ctx); err != nil {
		t.Fatalf("create model api key: %v", err)
	}

	repo := &modelRepo{db: client}
	got, err := repo.Get(ctx, userID, modelID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(got.Edges.Apikeys) != 0 {
		t.Fatalf("apikey count = %d, want 0", len(got.Edges.Apikeys))
	}
	if got.Edges.User == nil {
		t.Fatal("user edge is nil, want existing WithUser behavior preserved")
	}
}

func TestModelRepoCreateRuntimeAPIKeyScopesUserModelAndVM(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:model-repo-create-runtime-key?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })

	userID := uuid.New()
	otherUserID := uuid.New()
	modelID := uuid.New()
	vmID := "vm-runtime"

	for _, u := range []uuid.UUID{userID, otherUserID} {
		if _, err := client.User.Create().
			SetID(u).
			SetName("user").
			SetRole(consts.UserRoleIndividual).
			SetStatus(consts.UserStatusActive).
			Save(ctx); err != nil {
			t.Fatalf("create user: %v", err)
		}
	}
	if _, err := client.Model.Create().
		SetID(modelID).
		SetUserID(userID).
		SetProvider("OpenAI").
		SetAPIKey("model-key").
		SetBaseURL("https://model.example/v1").
		SetModel("gpt-4.1").
		Save(ctx); err != nil {
		t.Fatalf("create model: %v", err)
	}

	repo := &modelRepo{db: client}
	key, err := repo.CreateRuntimeAPIKey(ctx, userID, modelID, vmID)
	if err != nil {
		t.Fatalf("CreateRuntimeAPIKey() error = %v", err)
	}
	if key == "" || key == "model-key" {
		t.Fatalf("runtime key = %q, want generated token", key)
	}

	keys, err := client.ModelApiKey.Query().All(ctx)
	if err != nil {
		t.Fatalf("query keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("key count = %d, want 1", len(keys))
	}
	if keys[0].UserID != userID || keys[0].ModelID != modelID || keys[0].VirtualmachineID != vmID || keys[0].APIKey != key {
		t.Fatalf("key = %+v, want scoped runtime key", keys[0])
	}
}

func TestModelRepoGetAllowsAdminBuiltinModel(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:model-repo-get-admin-builtin?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })

	userID := uuid.New()
	adminID := uuid.New()
	modelID := uuid.New()

	createModelTestUser(t, ctx, client, userID, consts.UserRoleIndividual)
	createModelTestUser(t, ctx, client, adminID, consts.UserRoleAdmin)
	if _, err := client.Model.Create().
		SetID(modelID).
		SetUserID(adminID).
		SetProvider("OpenAI").
		SetAPIKey("model-key").
		SetBaseURL("https://model.example/v1").
		SetModel("gpt-4.1").
		Save(ctx); err != nil {
		t.Fatalf("create model: %v", err)
	}

	repo := &modelRepo{db: client}
	got, err := repo.Get(ctx, userID, modelID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.ID != modelID || got.UserID != adminID {
		t.Fatalf("model = %+v, want admin builtin model", got)
	}
}

func TestModelRepoCreateRuntimeAPIKeyAllowsAdminBuiltinModel(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:model-repo-runtime-key-admin-builtin?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })

	userID := uuid.New()
	adminID := uuid.New()
	modelID := uuid.New()
	vmID := "vm-builtin"

	createModelTestUser(t, ctx, client, userID, consts.UserRoleIndividual)
	createModelTestUser(t, ctx, client, adminID, consts.UserRoleAdmin)
	if _, err := client.Model.Create().
		SetID(modelID).
		SetUserID(adminID).
		SetProvider("OpenAI").
		SetAPIKey("model-key").
		SetBaseURL("https://model.example/v1").
		SetModel("gpt-4.1").
		Save(ctx); err != nil {
		t.Fatalf("create model: %v", err)
	}

	repo := &modelRepo{db: client}
	key, err := repo.CreateRuntimeAPIKey(ctx, userID, modelID, vmID)
	if err != nil {
		t.Fatalf("CreateRuntimeAPIKey() error = %v", err)
	}

	keys, err := client.ModelApiKey.Query().All(ctx)
	if err != nil {
		t.Fatalf("query keys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("key count = %d, want 1", len(keys))
	}
	if keys[0].UserID != userID || keys[0].ModelID != modelID || keys[0].VirtualmachineID != vmID || keys[0].APIKey != key {
		t.Fatalf("key = %+v, want runtime key for requesting user and builtin model", keys[0])
	}
}

func TestModelRepoCreateRuntimeAPIKeyDoesNotRedirectActiveModelOnRejectedSwitch(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:model-repo-reuse-runtime-key?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })

	userID := uuid.New()
	adminID := uuid.New()
	oldModelID := uuid.New()
	targetModelID := uuid.New()
	keyID := uuid.New()
	vmID := "vm-reuse-runtime"
	runtimeKey := "existing-runtime-key"

	createModelTestUser(t, ctx, client, userID, consts.UserRoleIndividual)
	createModelTestUser(t, ctx, client, adminID, consts.UserRoleAdmin)
	if _, err := client.Model.Create().
		SetID(oldModelID).
		SetUserID(userID).
		SetProvider("OpenAI").
		SetAPIKey("old-model-key").
		SetBaseURL("https://old.example/v1").
		SetModel("gpt-4.1").
		Save(ctx); err != nil {
		t.Fatalf("create old model: %v", err)
	}
	if _, err := client.Model.Create().
		SetID(targetModelID).
		SetUserID(adminID).
		SetProvider("OpenAI").
		SetAPIKey("target-model-key").
		SetBaseURL("https://target.example/v1").
		SetModel("gpt-5").
		Save(ctx); err != nil {
		t.Fatalf("create target model: %v", err)
	}
	if _, err := client.ModelApiKey.Create().
		SetID(keyID).
		SetUserID(userID).
		SetModelID(oldModelID).
		SetVirtualmachineID(vmID).
		SetAPIKey(runtimeKey).
		Save(ctx); err != nil {
		t.Fatalf("create existing runtime key: %v", err)
	}

	repo := &modelRepo{db: client}
	key, err := repo.CreateRuntimeAPIKey(ctx, userID, targetModelID, vmID)
	if err != nil {
		t.Fatalf("CreateRuntimeAPIKey() error = %v", err)
	}
	if key == "" || key == runtimeKey {
		t.Fatalf("runtime key = %q, want an independent target-model key", key)
	}
	// A second model switch acquires its key before durable admission can
	// reject it for a pending restart. Neither this acquisition nor a retry
	// may mutate the credential used by the original active/pending Run.
	for _, selection := range []struct {
		model uuid.UUID
		key   string
	}{{oldModelID, runtimeKey}, {targetModelID, key}, {oldModelID, runtimeKey}} {
		reused, err := repo.CreateRuntimeAPIKey(ctx, userID, selection.model, vmID)
		if err != nil || reused != selection.key {
			t.Fatalf("repeat model selection returned %q, %v; want original model-bound key", reused, err)
		}
	}

	keys, err := client.ModelApiKey.Query().All(ctx)
	if err != nil {
		t.Fatalf("query keys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("key count = %d, want 2 model-bound keys", len(keys))
	}
	got, err := client.ModelApiKey.Query().Where(modelapikey.ID(keyID)).Only(ctx)
	if err != nil {
		t.Fatalf("query reused key: %v", err)
	}
	if got.APIKey != runtimeKey || got.UserID != userID || got.VirtualmachineID != vmID || got.ModelID != oldModelID {
		t.Fatalf("key = %+v, want original model binding unchanged", got)
	}
	target, err := client.ModelApiKey.Query().Where(modelapikey.APIKey(key)).Only(ctx)
	if err != nil {
		t.Fatalf("query target key: %v", err)
	}
	if target.ModelID != targetModelID || target.UserID != userID || target.VirtualmachineID != vmID || target.Kind != modelapikey.KindRuntime {
		t.Fatalf("key = %+v, want target-model runtime key", target)
	}
}

func TestModelRepoCreateRuntimeAPIKeyDoesNotReuseForeignOrNonRuntimeKey(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:model-repo-key-scope?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	userID, otherUserID, modelID := uuid.New(), uuid.New(), uuid.New()
	createModelTestUser(t, ctx, client, userID, consts.UserRoleIndividual)
	createModelTestUser(t, ctx, client, otherUserID, consts.UserRoleIndividual)
	client.Model.Create().SetID(modelID).SetUserID(userID).SetProvider("OpenAI").
		SetAPIKey("fixture-model-key").SetBaseURL("https://model.example/v1").SetModel("fixture-model").SaveX(ctx)
	for _, unrelated := range []struct {
		owner uuid.UUID
		vm    string
		kind  modelapikey.Kind
		key   string
	}{
		{otherUserID, "vm-scoped", modelapikey.KindRuntime, "foreign-owner-key"},
		{userID, "different-vm", modelapikey.KindRuntime, "foreign-vm-key"},
		{userID, "vm-scoped", modelapikey.KindOhmyagent, "non-runtime-key"},
	} {
		client.ModelApiKey.Create().SetID(uuid.New()).SetUserID(unrelated.owner).SetModelID(modelID).
			SetVirtualmachineID(unrelated.vm).SetKind(unrelated.kind).SetAPIKey(unrelated.key).SaveX(ctx)
	}
	repo := &modelRepo{db: client}
	key, err := repo.CreateRuntimeAPIKey(ctx, userID, modelID, "vm-scoped")
	if err != nil {
		t.Fatalf("CreateRuntimeAPIKey() error = %v", err)
	}
	if key == "" || key == "foreign-owner-key" || key == "foreign-vm-key" || key == "non-runtime-key" {
		t.Fatalf("unexpected reused key: %q", key)
	}
	again, err := repo.CreateRuntimeAPIKey(ctx, userID, modelID, "vm-scoped")
	if err != nil || again != key {
		t.Fatalf("same scoped request returned %q, %v; want %q", again, err, key)
	}
	if count := client.ModelApiKey.Query().CountX(ctx); count != 4 {
		t.Fatalf("key count = %d, want 3 unrelated keys and 1 reusable runtime key", count)
	}
	// Normal revocation must not resurrect a previously selected model key.
	if _, err := client.ModelApiKey.Delete().Where(modelapikey.APIKey(key)).Exec(ctx); err != nil {
		t.Fatalf("revoke runtime key: %v", err)
	}
	replacement, err := repo.CreateRuntimeAPIKey(ctx, userID, modelID, "vm-scoped")
	if err != nil || replacement == "" || replacement == key {
		t.Fatalf("revoked key was reused: %q, %v", replacement, err)
	}
}

func TestModelRepoCreateAndDeleteOhMyAgentAPIKey(t *testing.T) {
	client := enttest.Open(t, "sqlite3", "file:model-repo-ohmyagent-key?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	userID := uuid.New()
	repo := &modelRepo{db: client}

	key, err := repo.CreateOhMyAgentAPIKey(ctx, userID)
	if err != nil {
		t.Fatalf("CreateOhMyAgentAPIKey() error = %v", err)
	}
	if key.UserID != userID || key.Kind != modelapikey.KindOhmyagent || key.ModelID != uuid.Nil || key.VirtualmachineID != "" || !strings.HasPrefix(key.APIKey, "oma_") || !strings.HasPrefix(key.SigningSecret, "omas_") {
		t.Fatalf("key = %+v, want model-independent ohmyagent key", key)
	}
	if err := repo.DeleteOhMyAgentAPIKey(ctx, uuid.New(), key.ID); err != nil {
		t.Fatalf("delete key as another user: %v", err)
	}
	if _, err := client.ModelApiKey.Get(ctx, key.ID); err != nil {
		t.Fatalf("key should still exist after another user deletes it: %v", err)
	}
	if err := repo.DeleteOhMyAgentAPIKey(ctx, userID, key.ID); err != nil {
		t.Fatalf("DeleteOhMyAgentAPIKey() error = %v", err)
	}
	if _, err := client.ModelApiKey.Get(ctx, key.ID); !db.IsNotFound(err) {
		t.Fatalf("deleted key lookup error = %v, want not found", err)
	}
}

func createModelTestUser(t *testing.T, ctx context.Context, client *db.Client, id uuid.UUID, role consts.UserRole) {
	t.Helper()
	if _, err := client.User.Create().
		SetID(id).
		SetName("user").
		SetRole(role).
		SetStatus(consts.UserStatusActive).
		Save(ctx); err != nil {
		t.Fatalf("create user: %v", err)
	}
}
