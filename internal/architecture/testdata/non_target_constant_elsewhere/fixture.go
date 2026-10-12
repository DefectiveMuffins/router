package nontargetconstantelsewhere

// ClientAppOpencode reuses a reviewed name outside its reviewed package, so it
// is still a feature target constant.
const ClientAppOpencode = "opencode"

// Value keeps the constant referenced.
func Value() string { return ClientAppOpencode }
