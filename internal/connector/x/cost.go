package x

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// CostTracker exposes Prometheus metrics for the X pay-per-use spend
// model introduced February 2026. Per §17 ("X pay-per-use cost runaway")
// we must alert at 70% of budget; this is the data plane that the
// supervisor dashboard and alerting rules consume.
//
// Numbers come from Blotato's April 2026 rate card cited in §4.2:
//
//	Standard write       $0.015 / post
//	Post containing URL  $0.20  / post  ← 1900% premium, watch closely
//	Read                 $0.005 / read  ($0.001 for owned)
//	Cap                  2 000 000 reads / month before Enterprise
const (
	UnitCostWriteStandard = 0.015
	UnitCostWriteWithURL  = 0.20
	UnitCostRead          = 0.005
	UnitCostReadOwned     = 0.001
)

// MonthlyReadCap is the inclusive cap before Enterprise pricing kicks in.
const MonthlyReadCap = 2_000_000

// SpendUSD counts X spend by tenant + operation. Sum-by-tenant gives
// per-customer cost telemetry; sum-by-operation drives the hard-spend
// alert on URL-bearing writes.
var SpendUSD = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "contactcentre",
	Subsystem: "x",
	Name:      "spend_usd_total",
	Help:      "Cumulative X API spend in USD, per tenant and operation kind.",
}, []string{"tenant", "operation"})

// ReadsTotal counts API reads per tenant. The supervisor dashboard
// shows "X reads this month" against MonthlyReadCap so a spend cap can
// kick in *before* X moves us to Enterprise pricing.
var ReadsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "contactcentre",
	Subsystem: "x",
	Name:      "reads_total",
	Help:      "Cumulative X API reads, per tenant. Compare against MonthlyReadCap.",
}, []string{"tenant"})

// RecordWrite logs the cost of an outbound write. Pass true for
// containsURL when the post text includes any link — the cost jumps 13×
// and we want spend telemetry to reflect that immediately.
func RecordWrite(tenantID string, containsURL bool) {
	cost := UnitCostWriteStandard
	op := "write_standard"
	if containsURL {
		cost = UnitCostWriteWithURL
		op = "write_with_url"
	}
	SpendUSD.WithLabelValues(tenantID, op).Add(cost)
}

// RecordRead logs a read against the per-month cap and the spend total.
// Pass true for owned when the read is against an owned resource
// (own posts, bookmarks, followers) which is priced cheaper.
func RecordRead(tenantID string, owned bool) {
	cost := UnitCostRead
	op := "read"
	if owned {
		cost = UnitCostReadOwned
		op = "read_owned"
	}
	SpendUSD.WithLabelValues(tenantID, op).Add(cost)
	ReadsTotal.WithLabelValues(tenantID).Inc()
}
