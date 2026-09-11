package service

import (
	"errors"
	"fmt"
	"github.com/QuantumNous/new-api/relaykit/types"
	"net/http"
	"testing"
)

func TestBillingQuotaContext(t *testing.T) {
	for _, code := range []types.ErrorCode{"personal_quota_insufficient", "team_quota_insufficient", "team_quota_personal_fallback_disabled", "team_and_personal_quota_insufficient"} {
		original := types.NewErrorWithStatusCode(fmt.Errorf("%w: zero", ErrInsufficientWalletQuota), types.ErrorCodeInsufficientUserQuota, http.StatusForbidden, types.ErrOptionWithSkipRetry(), types.ErrOptionWithNoRecordErrorLog())
		got := describeQuotaFailure(original, code)
		if got.GetErrorCode() != code || got.StatusCode != 403 || !errors.Is(got, ErrInsufficientWalletQuota) || !types.IsSkipRetryError(got) || types.IsRecordErrorLog(got) {
			t.Fatal("quota classification changed failure semantics")
		}
		if got.ToOpenAIError().Code != code {
			t.Fatal("missing wire error code")
		}
	}
	for _, code := range []types.ErrorCode{types.ErrorCodeInsufficientUserQuota, "pre_consume_token_quota_failed", "query_data_error"} {
		original := types.NewErrorWithStatusCode(errors.New("billing account unavailable"), code, 403)
		if describeQuotaFailure(original, "team_quota_insufficient") != original {
			t.Fatal("non-shortage was relabeled")
		}
	}
}
