package connector

import (
	"time"

	"github.com/rs/zerolog"

	"go.mau.fi/mautrix-teams/internal/teams/auth"
)

// requestStatsInterval spaces the poll loop's report of how many requests
// the bridge made to Teams and Microsoft.
const requestStatsInterval = time.Hour

// logRequestStats logs the requests made over the last period (about
// requestStatsInterval), by kind, and starts a new tally.
func logRequestStats(log zerolog.Logger, period time.Duration) {
	counts, total, throttled := auth.TakeRequestStats()
	byKind := zerolog.Dict()
	for _, c := range counts {
		byKind.Int64(c.Kind, c.Count)
	}
	log.Info().
		Int64("total", total).
		Int64("throttled", throttled).
		Dur("period", period.Round(time.Minute)).
		Dict("by_kind", byKind).
		Msg("Teams requests since the last report")
}
