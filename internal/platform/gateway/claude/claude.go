// Package claude is the gateway's Anthropic provider, built on the official Go SDK.
// Only the gateway may import it (internal/archtest enforces this).
package claude

import (
	"context"
	"fmt"
	"strings"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/UncleSon21/vellatry/internal/platform/gateway"
)

// Price is USD per million tokens.
type Price struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

// DefaultPrices are Anthropic first-party rates. Cache writes cost 1.25x input and cache
// reads 0.1x input. A model without a price is refused rather than metered at zero.
var DefaultPrices = map[string]Price{
	"claude-haiku-4-5": {InputPerMTok: 1, OutputPerMTok: 5},
	"claude-sonnet-5":  {InputPerMTok: 2, OutputPerMTok: 10},
	"claude-opus-5":    {InputPerMTok: 5, OutputPerMTok: 25},
	"claude-opus-4-8":  {InputPerMTok: 5, OutputPerMTok: 25}, // a server-side fallback target
}

// Models that may decline for policy reasons get server-side refusal fallbacks, so a
// decline is re-served inside the same call instead of failing the user's request.
var fallbackModels = map[string]bool{"claude-opus-5": true, "claude-fable-5-1": true}

// Provider calls the Messages API.
type Provider struct {
	client sdk.Client
	prices map[string]Price
}

// New returns a Provider. An empty apiKey falls back to the SDK's credential resolution
// (ANTHROPIC_API_KEY and friends).
func New(apiKey string, prices map[string]Price, opts ...option.RequestOption) *Provider {
	var o []option.RequestOption
	if apiKey != "" {
		o = append(o, option.WithAPIKey(apiKey))
	}
	o = append(o, opts...)
	if prices == nil {
		prices = DefaultPrices
	}
	return &Provider{client: sdk.NewClient(o...), prices: prices}
}

// Complete implements gateway.Provider.
func (p *Provider) Complete(ctx context.Context, model string, maxOutputTokens int, req gateway.Request) (gateway.Response, error) {
	if _, ok := p.prices[model]; !ok {
		return gateway.Response{}, fmt.Errorf("claude: no price configured for %q", model)
	}
	params := sdk.BetaMessageNewParams{
		Model:     sdk.Model(model),
		MaxTokens: int64(maxOutputTokens),
	}
	if req.System != "" {
		// The system prompt is the stable prefix; cache it.
		params.System = []sdk.BetaTextBlockParam{{Text: req.System, CacheControl: sdk.NewBetaCacheControlEphemeralParam()}}
	}
	for _, m := range req.Messages {
		block := []sdk.BetaContentBlockParamUnion{sdk.NewBetaTextBlock(m.Content)}
		switch m.Role {
		case "user":
			params.Messages = append(params.Messages, sdk.BetaMessageParam{Role: sdk.BetaMessageParamRoleUser, Content: block})
		case "assistant":
			params.Messages = append(params.Messages, sdk.BetaMessageParam{Role: sdk.BetaMessageParamRoleAssistant, Content: block})
		default:
			return gateway.Response{}, fmt.Errorf("claude: unsupported role %q", m.Role)
		}
	}
	if req.Schema != nil {
		params.OutputConfig = sdk.BetaOutputConfigParam{Format: sdk.BetaJSONOutputFormatParam{Schema: req.Schema}}
	}
	if fallbackModels[model] {
		params.Fallbacks = sdk.BetaFallbacksParamOfDefault()
		params.Betas = append(params.Betas, sdk.AnthropicBetaServerSideFallback2026_07_01)
	}

	msg, err := p.client.Beta.Messages.New(ctx, params)
	if err != nil {
		return gateway.Response{}, fmt.Errorf("claude: %w", err)
	}
	switch msg.StopReason {
	case sdk.BetaStopReasonRefusal:
		return gateway.Response{}, gateway.ErrRefused
	case sdk.BetaStopReasonMaxTokens:
		return gateway.Response{}, gateway.ErrTruncated
	}

	var text strings.Builder
	for _, block := range msg.Content {
		if tb, ok := block.AsAny().(sdk.BetaTextBlock); ok {
			text.WriteString(tb.Text)
		}
	}
	served := string(msg.Model)
	price, ok := p.prices[served]
	if !ok {
		price = p.prices[model]
	}
	u := msg.Usage
	cost := (float64(u.InputTokens)*price.InputPerMTok +
		float64(u.CacheCreationInputTokens)*price.InputPerMTok*1.25 +
		float64(u.CacheReadInputTokens)*price.InputPerMTok*0.1 +
		float64(u.OutputTokens)*price.OutputPerMTok) / 1e6
	return gateway.Response{
		Text:  text.String(),
		Model: served,
		Usage: gateway.Usage{
			InputTokens:  int(u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens),
			OutputTokens: int(u.OutputTokens),
			CostUSD:      cost,
		},
	}, nil
}
