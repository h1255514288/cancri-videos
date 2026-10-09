package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
)

func TestApplicationRoutes(t *testing.T) {
	for _, tt := range []struct {
		role, path, method string
		ready              bool
		want               int
	}{
		{"control", "/health/live", "GET", false, 200},
		{"control", "/health/ready", "GET", true, 200},
		{"control", "/health/ready", "GET", false, 503},
		{"control", "/api/v1/claims", "POST", false, 401},
		{"node", "/health/live", "GET", false, 200},
		{"node", "/health/ready", "GET", true, 200},
		{"node", "/d/token", "GET", false, 401},
	} {
		t.Run(fmt.Sprintf("%s%s/%d", tt.role, tt.path, tt.want), func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if tt.path == "/health/ready" {
				p := mock.ExpectPing()
				if !tt.ready {
					p.WillReturnError(errors.New("not ready"))
				}
			}
			w := httptest.NewRecorder()
			logger := slog.Default()
			ApplicationHandler(tt.role, sqlx.NewDb(db, "sqlmock"), "test-secret", logger).ServeHTTP(w, httptest.NewRequest(tt.method, tt.path, nil))
			if w.Code != tt.want {
				t.Fatalf("got %d want %d: %s", w.Code, tt.want, w.Body.String())
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
