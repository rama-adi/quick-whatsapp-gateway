package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/domain"
	"github.com/ramaadi/quick-whatsapp-gateway/internal/store"
)

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
