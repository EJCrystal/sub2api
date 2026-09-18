package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

func TestFetchOpenAIAccountModelsOAuthPopulatesPickerFields(t *testing.T) {
	_, calls := newCodexModelsOAuthCacheServer(t, `{"models":[
		{"slug":"new-oauth-model","display_name":"New OAuth Model"},
		{"slug":"gpt-5.6-sol"},
		{"slug":"blank-display-name","display_name":"   "},
		{"slug":"gpt-6-astra"}
	]}`)
	gateway := &OpenAIGatewayService{}
	svc := &AccountTestService{openaiGatewayService: gateway}
	account := newCodexModelsTestAccount()
	ctx := context.Background()

	before, err := gateway.FetchOpenAIModelsList(ctx, account)
	require.NoError(t, err)
	models, err := svc.FetchOpenAIAccountModels(ctx, account)
	require.NoError(t, err)
	require.Greater(t, len(models), 2)
	// Upstream display name wins; a missing one falls back to the local catalog name,
	// then to the raw slug.
	for i, expected := range []struct{ id, displayName string }{
		{id: "new-oauth-model", displayName: "New OAuth Model"},
		{id: "gpt-5.6-sol", displayName: "GPT-5.6 Sol"},
		{id: "blank-display-name", displayName: "blank-display-name"},
		{id: "gpt-6-astra", displayName: "GPT-6 Astra"},
	} {
		require.Equal(t, expected.id, models[i].ID)
		require.Equal(t, expected.displayName, models[i].DisplayName)
		require.Equal(t, "model", models[i].Type)
	}
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	require.Contains(t, ids, "gpt-image-2.5-flare")
	require.Contains(t, ids, "gpt-image-2.5-sunburst")

	after, err := gateway.FetchOpenAIModelsList(ctx, account)
	require.NoError(t, err)
	require.Equal(t, before.Body, after.Body, "picker fields must not change the shared catalog")
	require.Contains(t, string(after.Body), `"display_name":"New OAuth Model"`, "the shared catalog keeps the upstream display name")
	require.EqualValues(t, 1, calls.Load(), "picker must reuse the shared discovery cache")
}

func TestFetchOpenAIAccountModelsAPIKeyPopulatesPickerFields(t *testing.T) {
	gateway := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		return ordinaryModelsUpstreamResponse(`{"data":[
			{"id":"new-api-model","owned_by":"provider","created":123},
			{"id":"blank-label","display_name":"  ","type":""},
			{"id":"named-model","display_name":"Provider Model","type":"model"}
		]}`), nil
	}})
	svc := &AccountTestService{openaiGatewayService: gateway}
	models, err := svc.FetchOpenAIAccountModels(context.Background(), newCodexModelsAPIKeyTestAccount("https://models.example/v1"))
	require.NoError(t, err)
	require.Len(t, models, 3)
	for i, name := range []string{"new-api-model", "blank-label", "Provider Model"} {
		require.Equal(t, name, models[i].DisplayName)
		require.Equal(t, "model", models[i].Type)
	}
	require.Equal(t, "provider", models[0].OwnedBy)
	require.EqualValues(t, 123, models[0].Created)
	require.Equal(t, "named-model", models[2].ID)
}

func TestFetchOpenAIAccountModelsAPIKeyRespectsModelRestriction(t *testing.T) {
	upstream := func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		return ordinaryModelsUpstreamResponse(`{"data":[
			{"id":"claude-opus-5","owned_by":"provider"},
			{"id":"gpt-5.6-sol"},
			{"id":"gpt-6-astra"},
			{"id":"deepseek-v4-flash"}
		]}`), nil
	}
	// 上游供了 4 个模型，账号模型限制只允许 2 个（白名单 = identity 映射）→ 下拉只列这 2 个。
	gateway := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: upstream})
	svc := &AccountTestService{openaiGatewayService: gateway}
	account := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	account.Credentials["model_mapping"] = map[string]any{
		"gpt-5.6-sol":      "gpt-5.6-sol",
		"allowed-upstream": "allowed-upstream", // 上游目录里没有，仍须可选
	}
	models, err := svc.FetchOpenAIAccountModels(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, []string{"gpt-5.6-sol", "allowed-upstream"}, modelIDs(models))

	// 通配符限制：只留上游命中的条目，且不把字面 "claude-*" 当作可测模型。
	wildcard := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	wildcard.Credentials["model_mapping"] = map[string]any{"claude-*": "claude-opus-5"}
	wildcardGateway := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: upstream})
	wildcardModels, err := (&AccountTestService{openaiGatewayService: wildcardGateway}).
		FetchOpenAIAccountModels(context.Background(), wildcard)
	require.NoError(t, err)
	require.Equal(t, []string{"claude-opus-5"}, modelIDs(wildcardModels))

	// 无模型限制 = 允许所有模型，列表保持上游全量（行为不变）。
	open := newCodexModelsAPIKeyTestAccount("https://models.example/v1")
	openModels, err := (&AccountTestService{openaiGatewayService: newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: upstream})}).
		FetchOpenAIAccountModels(context.Background(), open)
	require.NoError(t, err)
	require.Len(t, openModels, 4)
}

func modelIDs(models []openai.Model) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	return ids
}

func TestFetchOpenAIAccountModelsPreservesEmptyCatalog(t *testing.T) {
	gateway := newCodexModelsAPIKeyTestService(&codexModelsHTTPUpstreamStub{do: func(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
		return ordinaryModelsUpstreamResponse(`{"data":[]}`), nil
	}})
	svc := &AccountTestService{openaiGatewayService: gateway}
	models, err := svc.FetchOpenAIAccountModels(context.Background(), newCodexModelsAPIKeyTestAccount("https://models.example/v1"))
	require.NoError(t, err)
	require.Empty(t, models, "an empty upstream catalog must not become a static model list")
}

func TestFetchOpenAIAccountModelsOAuthLabelsLocalImageModelsLikeUpstream(t *testing.T) {
	newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"gpt-5.6-sol"}]}`)
	svc := &AccountTestService{openaiGatewayService: &OpenAIGatewayService{}}
	account := newCodexModelsTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-image-2.5-flare": "gpt-image-2.5-flare"}
	models, err := svc.FetchOpenAIAccountModels(context.Background(), account)
	require.NoError(t, err)
	byID := make(map[string]string, len(models))
	for _, model := range models {
		byID[model.ID] = model.DisplayName
	}
	require.Equal(t, "GPT-5.6 Sol", byID["gpt-5.6-sol"], "upstream slug must not be the only label source")
	require.Equal(t, "GPT Image 2.5 Flare", byID["gpt-image-2.5-flare"], "locally added models use the same naming rule")
}

func TestFetchOpenAIAccountModelsOAuthRespectsImageAllowlist(t *testing.T) {
	newCodexModelsOAuthCacheServer(t, `{"models":[{"slug":"gpt-6-astra"}]}`)
	svc := &AccountTestService{openaiGatewayService: &OpenAIGatewayService{}}
	account := newCodexModelsTestAccount()
	account.Credentials["model_mapping"] = map[string]any{"gpt-image-2.5-flare": "gpt-image-2.5-flare"}
	models, err := svc.FetchOpenAIAccountModels(context.Background(), account)
	require.NoError(t, err)
	ids := []string{}
	for _, model := range models {
		ids = append(ids, model.ID)
	}
	require.Contains(t, ids, "gpt-image-2.5-flare")
	require.NotContains(t, ids, "gpt-image-2.5-sunburst")
}
