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
		return "Search the internet for current information, news, and web pages."
	case "web_fetch":
		return "Fetch the full content of a URL (articles, docs, web pages)."
	case "web_code":
		return "Search for code examples, docs, and repositories."
	case "web_company":
		return "Research companies, products, funding, and team info."
	case "web_research":
		return "Deep research on a topic using multiple sources."
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
