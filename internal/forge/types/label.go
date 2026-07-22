package forgetypes

// Label represents a forge repository label.
type Label struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}
