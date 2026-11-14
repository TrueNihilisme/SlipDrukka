package winsvc

// FirewallRule is what one Windows firewall rule admits, as the admin page
// shows it. "Any" in Ports or Remote means no limit.
type FirewallRule struct {
	Name     string   `json:"name"`
	Protocol string   `json:"protocol"`
	Ports    []string `json:"ports"`
	Remote   []string `json:"remote"`
}
