package apitypes

// OverviewRange is the length of the overview window.
type OverviewRange string

const (
	OverviewRange24h OverviewRange = "24h"
	OverviewRange7d  OverviewRange = "7d"
	OverviewRange14d OverviewRange = "14d"
	OverviewRange30d OverviewRange = "30d"
)

// Overview is the GET /overview response: activity totals for one window plus
// the same counts split into time buckets for charting.
type Overview struct {
	Range          OverviewRange  `json:"range" enum:"24h,7d,14d,30d" doc:"The window length that was used: 24h returns 24 hourly buckets; 7d, 14d and 30d return 7, 14 or 30 daily buckets." example:"7d"`
	TZ             string         `json:"tz" doc:"The IANA time zone the buckets are aligned to (echoes the tz parameter, UTC by default). Hourly buckets start on the hour and daily buckets start at local midnight in this zone." example:"Asia/Jakarta"`
	From           int64          `json:"from" doc:"Start of the window in Unix epoch milliseconds. Equal to the start of the first bucket." example:"1760029200000"`
	To             int64          `json:"to" doc:"End of the window in Unix epoch milliseconds: the moment the summary was computed. Activity at or after this instant is not counted." example:"1760600000000"`
	Buckets        []int64        `json:"buckets" nullable:"false" doc:"Start of each bucket in Unix epoch milliseconds, oldest first. Each bucket runs until the next bucket starts; the last bucket runs until to and is therefore usually partial. Daily buckets follow the local calendar, so a bucket on a daylight-saving change day is 23 or 25 hours long." example:"[1760029200000,1760115600000,1760202000000,1760288400000,1760374800000,1760461200000,1760547600000]"`
	Totals         OverviewTotals `json:"totals" doc:"Counts for the whole window."`
	Series         OverviewSeries `json:"series" doc:"The same counts split per bucket. Every array has exactly one entry per element of buckets, in the same order; buckets with no activity are 0."`
	HasSentMessage bool           `json:"hasSentMessage" doc:"True when the organization has ever sent a WhatsApp message, either through the API or from a linked phone. Not limited to the window or the session filter. Useful for a getting-started checklist." example:"true"`
	RetentionDays  int            `json:"retentionDays" doc:"The configured history retention in days (the RETENTION_DAYS setting). 0 means history is kept forever. When the window is longer than this value, the oldest buckets may be undercounted because older messages and deliveries can be deleted." example:"0"`
}

// OverviewTotals are window-wide counts.
type OverviewTotals struct {
	MessagesReceived  int64 `json:"messagesReceived" doc:"Messages received from WhatsApp (incoming) whose WhatsApp timestamp is in the window." example:"1234"`
	MessagesSent      int64 `json:"messagesSent" doc:"Outgoing messages whose WhatsApp timestamp is in the window. Includes messages sent through the API and messages typed on the linked phone." example:"567"`
	ActiveChats       int64 `json:"activeChats" doc:"Number of distinct chats (per session) with at least one incoming or outgoing message in the window." example:"42"`
	SendsFailed       int64 `json:"sendsFailed" doc:"Send requests created in the window that ended in a definite failure (outbox status failed). Sends with an unknown outcome are not counted." example:"3"`
	WebhookDeliveries int64 `json:"webhookDeliveries" doc:"Webhook deliveries created in the window that your endpoint accepted (status delivered)." example:"1100"`
	WebhookFailures   int64 `json:"webhookFailures" doc:"Webhook deliveries created in the window whose latest attempt failed: still retrying (failed) or given up (dead)." example:"13"`
}

// OverviewSeries holds per-bucket counts aligned with Overview.Buckets.
type OverviewSeries struct {
	MessagesReceived  []int64 `json:"messagesReceived" nullable:"false" doc:"Incoming messages per bucket." example:"[180,201,164,190,175,166,158]"`
	MessagesSent      []int64 `json:"messagesSent" nullable:"false" doc:"Outgoing messages per bucket." example:"[80,92,77,85,81,79,73]"`
	SendsFailed       []int64 `json:"sendsFailed" nullable:"false" doc:"Failed send requests per bucket, by creation time." example:"[0,1,0,0,2,0,0]"`
	WebhookDeliveries []int64 `json:"webhookDeliveries" nullable:"false" doc:"Delivered webhook deliveries per bucket, by creation time." example:"[160,170,150,165,155,152,148]"`
	WebhookFailures   []int64 `json:"webhookFailures" nullable:"false" doc:"Failed or dead webhook deliveries per bucket, by creation time." example:"[2,1,0,4,3,2,1]"`
}
