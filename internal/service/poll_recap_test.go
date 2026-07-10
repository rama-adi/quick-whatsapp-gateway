package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

// TestBuildPollRecapPayload_CountsLatestVotePerVoter builds a recap from multiple votes where one voter
// changes selection. Only each voters latest row may contribute to counts, totals, and optional voter
// detail. This prevents historical vote changes from inflating poll results.
func TestBuildPollRecapPayload_CountsLatestVotePerVoter(t *testing.T) {
	poll := domain.PollRecapCandidate{
		SessionID:       "sess_1",
		OrganizationID:  "org_1",
		PollMessageID:   "POLL1",
		ChatJID:         "group-test@g.us",
		Name:            "Lunch",
		Options:         []string{"Pizza", "Sushi"},
		SelectableCount: 1,
		EndTime:         1782554805000,
	}
	votes := []domain.PollVote{
		{
			ID:              1,
			SessionID:       "sess_1",
			PollMessageID:   "POLL1",
			VoterLID:        "111@lid",
			SelectedOptions: json.RawMessage(`["Pizza"]`),
			Timestamp:       10,
		},
		{
			ID:              2,
			SessionID:       "sess_1",
			PollMessageID:   "POLL1",
			VoterLID:        "222@lid",
			SelectedOptions: json.RawMessage(`["Sushi"]`),
			Timestamp:       10,
		},
		{
			ID:              3,
			SessionID:       "sess_1",
			PollMessageID:   "POLL1",
			VoterLID:        "111@lid",
			SelectedOptions: json.RawMessage(`["Sushi"]`),
			Timestamp:       11,
		},
	}

	got, err := buildPollRecapPayload(poll, votes, map[string]string{
		"111": "Ada",
		"222": "Bea",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalVotes != 2 {
		t.Fatalf("TotalVotes = %d, want 2", got.TotalVotes)
	}
	counts := pollOptionCounts(got.Options)
	if counts["Pizza"] != 0 || counts["Sushi"] != 2 {
		t.Fatalf("counts = %+v, want Pizza=0 Sushi=2", counts)
	}
	wantVoters := []domain.PollRecapVoter{
		{VoterID: "111@lid", DisplayName: "Ada", SelectedOptions: []string{"Sushi"}},
		{VoterID: "222@lid", DisplayName: "Bea", SelectedOptions: []string{"Sushi"}},
	}
	if !pollRecapVotersEqual(got.Voters, wantVoters) {
		t.Fatalf("voters = %+v, want %+v", got.Voters, wantVoters)
	}
}

// TestBuildPollRecapPayload_EmptyVoterKeysDoNotCollapse supplies legacy vote rows with empty voter LIDs.
// Each row must receive a distinct synthetic aggregation key instead of collapsing into one voter, while
// no fake key leaks into the public voter list. This preserves old data counts without inventing
// identities.
func TestBuildPollRecapPayload_EmptyVoterKeysDoNotCollapse(t *testing.T) {
	poll := domain.PollRecapCandidate{
		SessionID:      "sess_1",
		OrganizationID: "org_1",
		PollMessageID:  "POLL1",
		ChatJID:        "group-test@g.us",
		Name:           "Lunch",
		Options:        []string{"Pizza", "Sushi"},
	}
	votes := []domain.PollVote{
		{ID: 1, VoterLID: "", SelectedOptions: json.RawMessage(`["Pizza"]`), Timestamp: 10},
		{ID: 2, VoterLID: "", SelectedOptions: json.RawMessage(`["Sushi"]`), Timestamp: 10},
	}

	got, err := buildPollRecapPayload(poll, votes, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.TotalVotes != 2 {
		t.Fatalf("TotalVotes = %d, want 2", got.TotalVotes)
	}
	counts := pollOptionCounts(got.Options)
	if counts["Pizza"] != 1 || counts["Sushi"] != 1 {
		t.Fatalf("counts = %+v, want Pizza=1 Sushi=1", counts)
	}
}

// TestBuildPollRecapPayload_HideVotesOmitsVoters builds a recap for a poll configured to hide individual
// votes. Aggregate option counts and totals remain available, but the voter detail list must be absent.
// The privacy flag is enforced at event construction, before realtime and webhook fan-out.
func TestBuildPollRecapPayload_HideVotesOmitsVoters(t *testing.T) {
	poll := domain.PollRecapCandidate{
		SessionID:      "sess_1",
		OrganizationID: "org_1",
		PollMessageID:  "POLL1",
		ChatJID:        "group-test@g.us",
		Name:           "Lunch",
		Options:        []string{"Pizza", "Sushi"},
		HideVotes:      true,
	}
	votes := []domain.PollVote{
		{ID: 1, VoterLID: "111@lid", SelectedOptions: json.RawMessage(`["Pizza"]`), Timestamp: 10},
	}

	got, err := buildPollRecapPayload(poll, votes, map[string]string{"111": "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Voters != nil {
		t.Fatalf("Voters = %+v, want nil", got.Voters)
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["voters"]; ok {
		t.Fatalf("serialized payload includes voters: %s", raw)
	}
}

// TestBuildPollRecapPayload_MissingIdentityStillIncludesVoter builds a visible-vote recap when an identity
// lookup cannot provide a display name. The voter and selections must remain in the payload with an empty
// name rather than being dropped. Identity enrichment is optional; vote accounting is authoritative.
func TestBuildPollRecapPayload_MissingIdentityStillIncludesVoter(t *testing.T) {
	poll := domain.PollRecapCandidate{
		SessionID:      "sess_1",
		OrganizationID: "org_1",
		PollMessageID:  "POLL1",
		ChatJID:        "group-test@g.us",
		Name:           "Lunch",
		Options:        []string{"Pizza"},
	}
	votes := []domain.PollVote{
		{ID: 1, VoterLID: "111@lid", SelectedOptions: json.RawMessage(`["Pizza"]`), Timestamp: 10},
	}

	got, err := buildPollRecapPayload(poll, votes, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.PollRecapVoter{
		{VoterID: "111@lid", DisplayName: "", SelectedOptions: []string{"Pizza"}},
	}
	if !pollRecapVotersEqual(got.Voters, want) {
		t.Fatalf("voters = %+v, want %+v", got.Voters, want)
	}
}

// TestPollRecapWorkerBuildPayload_ResolvesVoterNamesFromIdentityRepo loads poll votes through the worker
// and resolves their LIDs against the identity repository at recap time. The emitted voter entries must
// use current identity names while preserving vote selections. Read-time resolution ensures later contact
// renames appear without rewriting vote rows.
func TestPollRecapWorkerBuildPayload_ResolvesVoterNamesFromIdentityRepo(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	poll := domain.PollRecapCandidate{
		SessionID:       "sess_1",
		OrganizationID:  "org_1",
		PollMessageID:   "POLL1",
		ChatJID:         "group-test@g.us",
		Name:            "Lunch",
		Options:         []string{"Pizza", "Sushi"},
		SelectableCount: 1,
		EndTime:         1782554805000,
	}
	voteRows := sqlmock.NewRows([]string{
		"id", "session_id", "poll_message_id", "voter_lid", "selected_options", "timestamp", "raw_json",
	}).
		AddRow(1, "sess_1", "POLL1", "222@lid", []byte(`["Sushi"]`), int64(10), []byte("")).
		AddRow(2, "sess_1", "POLL1", "111@lid", []byte(`["Pizza"]`), int64(11), []byte(""))
	mock.ExpectQuery("SELECT id, session_id, poll_message_id, voter_lid, selected_options, timestamp, COALESCE").
		WithArgs("sess_1", "POLL1").
		WillReturnRows(voteRows)
	identityRows := sqlmock.NewRows([]string{"lid", "phone_jid", "name"}).
		AddRow("111@lid", nil, "Ada")
	mock.ExpectQuery("SELECT lid, phone_jid, name").
		WithArgs("222@lid,111@lid", "222@lid,111@lid").
		WillReturnRows(identityRows)

	worker := &PollRecapWorker{
		votes: store.NewPollVoteRepo(db),
		ids:   store.NewIdentityRepo(db),
	}
	got, err := worker.buildPayload(context.Background(), poll)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.PollRecapVoter{
		{VoterID: "111@lid", DisplayName: "Ada", SelectedOptions: []string{"Pizza"}},
		{VoterID: "222@lid", DisplayName: "", SelectedOptions: []string{"Sushi"}},
	}
	if !pollRecapVotersEqual(got.Voters, want) {
		t.Fatalf("voters = %+v, want %+v", got.Voters, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func pollOptionCounts(options []domain.PollRecapOption) map[string]int {
	counts := make(map[string]int, len(options))
	for _, option := range options {
		counts[option.Option] = option.Count
	}
	return counts
}

func pollRecapVotersEqual(got, want []domain.PollRecapVoter) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i].VoterID != want[i].VoterID || got[i].DisplayName != want[i].DisplayName {
			return false
		}
		if len(got[i].SelectedOptions) != len(want[i].SelectedOptions) {
			return false
		}
		for j := range got[i].SelectedOptions {
			if got[i].SelectedOptions[j] != want[i].SelectedOptions[j] {
				return false
			}
		}
	}
	return true
}
