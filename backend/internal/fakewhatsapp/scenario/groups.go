package scenario

// GroupRef resolves to the JID created earlier in the same story.
type GroupRef struct{ Alias string }

func (g GroupRef) Chat() string { return "@" + g.Alias }

type GroupCreate struct {
	Alias        string
	Name         string
	Participants []string
}

func (GroupCreate) isStep()         {}
func (g GroupCreate) Ref() GroupRef { return GroupRef{Alias: g.Alias} }
func Group(alias, name string, participants ...string) GroupCreate {
	return GroupCreate{Alias: alias, Name: name, Participants: participants}
}
func GroupWithUsers(alias, name string, users ...User) GroupCreate {
	phones := make([]string, 0, len(users))
	for _, user := range users {
		phones = append(phones, user.Phone)
	}
	return Group(alias, name, phones...)
}
