package tools

type webAlias struct {
	web  *WebTool
	name string
}

func (a *webAlias) Name() string {
	return a.name
}

func (a *webAlias) Description() string {
	switch a.name {
	case "web_search":
		return "Search the internet for current information, news, facts, and web pages. Supports general web search with multiple results."
	case "web_fetch":
		return "Fetch and retrieve the full content of a specific URL. Useful for reading articles, documentation, or any web page content."
	case "web_code":
		return "Search for code examples, documentation, repositories, and programming solutions across the web."
	case "web_company":
		return "Research companies, products, funding, team, and business information."
	case "web_research":
		return "Deep research on a topic using multiple sources for comprehensive information gathering."
	default:
		return a.web.Description()
	}
}

func (a *webAlias) Parameters() []Parameter {
	return a.web.Parameters()
}

func (a *webAlias) Execute(ctx interface{}, args map[string]any) ToolResult {
	newArgs := make(map[string]any, len(args)+1)
	for k, v := range args {
		newArgs[k] = v
	}
	newArgs["operation"] = a.name
	return a.web.Execute(ctx, newArgs)
}
