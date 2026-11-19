package agenttools

import "slices"

// Resource is a read-only document an agent can attach, backed by one read
// tool: reading the resource is calling the tool, so it has the same route,
// the same scope and the same projection and there is no second route list.
type Resource struct {
	URI         string
	Tool        string
	Description string
}

// Prompt is a canned starting point. Its Description is static like a tool's,
// Arguments are the names it takes, and Tools are the read tools it draws on.
// The text a prompt expands to is the transport's, written from these and from
// nothing a deployment contains.
type Prompt struct {
	Name        string
	Description string
	Arguments   []string
	Tools       []string
}

var resources = []Resource{
	{"retnd://status", "status", "The health of every backup set."},
	{"retnd://backup-sets", "backup_sets_list", "The configured backup sets."},
	{"retnd://backup-sets/{source}/{set}", "backup_set_show", "One backup set."},
	{"retnd://backups/{source}/{set}/{name}", "backup_show", "One backup and its placements."},
	{"retnd://activity", "activity", "The activity journal, newest first."},
	{"retnd://encryption", "encryption_state", "The encryption policy and whether a rotation is due."},
	{"retnd://assurance/due", "assurance_due", "The backups whose scrub or recovery test is due."},
}

var prompts = []Prompt{
	{
		Name:        "triage-health",
		Description: "Work out which backup sets need attention and why, reading health, quarantine and what is overdue.",
		Arguments:   []string{},
		Tools:       []string{"status", "quarantine_list", "assurance_due", "activity"},
	},
	{
		Name:        "explain-backup",
		Description: "Explain one backup: its state, where its copies are, how they are encrypted and what retention will do to it.",
		Arguments:   []string{"source", "set", "name"},
		Tools:       []string{"backup_show", "copies_show", "retention_preview"},
	},
	{
		Name:        "prepare-recovery-test",
		Description: "Plan a recovery test for one backup set: which copy it would use, whether that copy is sound, and the command a person would confirm.",
		Arguments:   []string{"source", "set"},
		Tools:       []string{"backup_set_show", "backups_list", "assurance_show"},
	},
}

// Resources reports the resources.
func Resources() []Resource { return slices.Clone(resources) }

// Prompts reports the prompts, with their slices copied.
func Prompts() []Prompt {
	out := make([]Prompt, len(prompts))
	for i, p := range prompts {
		p.Arguments, p.Tools = slices.Clone(p.Arguments), slices.Clone(p.Tools)
		out[i] = p
	}
	return out
}
