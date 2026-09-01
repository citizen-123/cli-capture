package tui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/citizen-123/cli-capture/internal/capture"
)

func TestResendSelectedStoresFailedExchange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "10")
		_, _ = io.WriteString(w, "short")
	}))
	defer srv.Close()

	original := capture.NewFlow("c", strings.TrimPrefix(srv.URL, "http://"))
	original.Protocol = capture.ProtoHTTP1
	original.Request = &capture.Message{
		Meta: map[string]string{"method": http.MethodGet, "path": "/"},
	}
	store := capture.NewStore()
	model := Model{
		store:    store,
		flows:    []*capture.Flow{original},
		fi:       newFilter(),
		selected: 0,
	}

	status := model.resendSelected()
	if !strings.Contains(status, "resend failed:") {
		t.Fatalf("status = %q, want resend failure", status)
	}
	stored := store.List()
	if len(stored) != 1 {
		t.Fatalf("stored flows = %d, want 1", len(stored))
	}
	if stored[0].Status != capture.StatusError || stored[0].Err == nil {
		t.Errorf("stored flow status = %v, err = %v", stored[0].Status, stored[0].Err)
	}
	if stored[0].Response != nil {
		t.Errorf("stored flow retained a partial response: %+v", stored[0].Response)
	}
}
