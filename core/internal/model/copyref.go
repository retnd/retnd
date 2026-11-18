package model

// CopyRef names one stored copy of an artifact: the artifact and the medium
// holding it. It is what a refusal that has to say WHICH copies (a key that
// is still needed, EPIC N #992) carries as data, and what the journal hands
// back for them.
type CopyRef struct {
	Artifact ArtifactID
	Medium   string
}

// String renders the copy the way an operator reads it.
func (c CopyRef) String() string { return c.Artifact.String() + " on " + c.Medium }
