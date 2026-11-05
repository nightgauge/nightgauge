package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	api "github.com/nightgauge/nightgauge/api/generated/go/platform"
)

// The platform's inquiry scorecard (#2911), read from GET /v1/inquiry/scorecard.
//
// The platform is the one place the scorecard is computed. Core decodes the
// numbers and shows them; it derives none of them, not even a ratio. The field
// names are the platform spec's, verbatim.

// InquiryImpact is the impact tally of a set of answers: overall, or one class.
type InquiryImpact struct {
	Answers      int            `json:"answers"`
	Impacted     int            `json:"impacted"`
	Unattributed int            `json:"unattributed"`
	None         int            `json:"none"`
	ImpactRatio  float64        `json:"impactRatio"`
	ByChange     map[string]int `json:"byChange"`
}

// InquiryClassImpact is the impact tally of one question class.
type InquiryClassImpact struct {
	Class string `json:"class"`
	InquiryImpact
}

// InquirySets counts the question sets in the window and how they ended.
type InquirySets struct {
	Total            int            `json:"total"`
	Answered         int            `json:"answered"`
	Waived           int            `json:"waived"`
	Scored           int            `json:"scored"`
	ByWaiveReason    map[string]int `json:"byWaiveReason"`
	WaiveRate        float64        `json:"waiveRate"`
	SkipRate         float64        `json:"skipRate"`
	QuestionSkipRate float64        `json:"questionSkipRate"`
}

// InquiryKills counts runs an answer stopped, and the spend that avoided.
type InquiryKills struct {
	Count           int     `json:"count"`
	SpendAvoidedUSD float64 `json:"spendAvoidedUsd"`
}

// InquiryHistoryBucket is one side of the cold-start vs history-rich split.
type InquiryHistoryBucket struct {
	Sets        int     `json:"sets"`
	Answers     int     `json:"answers"`
	ImpactRatio float64 `json:"impactRatio"`
}

// InquiryHistoryComparison compares answers asked with and without history.
type InquiryHistoryComparison struct {
	ColdStart   InquiryHistoryBucket `json:"coldStart"`
	HistoryRich InquiryHistoryBucket `json:"historyRich"`
}

// InquiryWeakClass is a class whose impact ratio fell under the floor.
type InquiryWeakClass struct {
	Class          string  `json:"class"`
	Answers        int     `json:"answers"`
	ImpactRatio    float64 `json:"impactRatio"`
	Floor          float64 `json:"floor"`
	Recommendation string  `json:"recommendation"`
}

// InquiryScorecard is the platform's inquiry scorecard for one window.
type InquiryScorecard struct {
	Window      string               `json:"window"`
	WindowStart string               `json:"windowStart"`
	WindowEnd   string               `json:"windowEnd"`
	Floor       float64              `json:"floor"`
	MinSample   int                  `json:"minSample"`
	Overall     InquiryImpact        `json:"overall"`
	ByClass     []InquiryClassImpact `json:"byClass"`
	// MedianTimeToAnswerSeconds is nil when the window has no answer to time.
	MedianTimeToAnswerSeconds *float64                 `json:"medianTimeToAnswerSeconds"`
	Sets                      InquirySets              `json:"sets"`
	Kills                     InquiryKills             `json:"kills"`
	UnattributedTargets       int                      `json:"unattributedTargets"`
	HistoryComparison         InquiryHistoryComparison `json:"historyComparison"`
	WeakClasses               []InquiryWeakClass       `json:"weakClasses"`
}

// Empty reports whether the window holds no inquiry answers at all.
func (s *InquiryScorecard) Empty() bool {
	return s.Overall.Answers == 0 && len(s.ByClass) == 0
}

// InquiryScorecardQuery narrows the read. A zero field is not sent, so the
// platform applies its own default (window 30d).
type InquiryScorecardQuery struct {
	// Window is a day count such as "30d".
	Window string
	// Floor is the impact ratio under which a class is weak, 0..1.
	Floor *float64
	// MinSample is the fewest answers a class needs to be judged weak.
	MinSample *int
}

func (q InquiryScorecardQuery) values() url.Values {
	v := url.Values{}
	if q.Window != "" {
		v.Set("window", q.Window)
	}
	if q.Floor != nil {
		v.Set("floor", strconv.FormatFloat(*q.Floor, 'f', -1, 64))
	}
	if q.MinSample != nil {
		v.Set("minSample", strconv.Itoa(*q.MinSample))
	}
	return v
}

// ReadInquiryScorecard reads the signed-in account's inquiry scorecard. The
// route accepts a session only: with none installed it is refused with
// ErrNoSession before anything is sent.
func (c *Client) ReadInquiryScorecard(ctx context.Context, q InquiryScorecardQuery) (*InquiryScorecard, error) {
	req, err := c.newRequest(ctx, requestSpec{
		Op:          api.OpInquiryScorecard,
		Query:       q.values(),
		Headers:     map[string]string{"Accept": "application/json"},
		SessionOnly: true,
	})
	if err != nil {
		return nil, fmt.Errorf("inquiry scorecard: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("inquiry scorecard: GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("inquiry scorecard: %s", describeErrorResponse(resp))
	}
	var body struct {
		Scorecard *InquiryScorecard `json:"scorecard"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("inquiry scorecard: decode: %w", err)
	}
	if body.Scorecard == nil {
		return nil, errors.New("inquiry scorecard: the answer carries no scorecard")
	}
	return body.Scorecard, nil
}
