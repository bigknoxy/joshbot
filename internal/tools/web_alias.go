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
		return "web_search: search the internet for current information, news, and web pages."
	case "web_fetch":
		return "web_fetch: fetch the full content of a URL (articles, docs, web pages)."
	case "web_code":
		return "web_code: search for code examples, docs, and repositories."
	case "web_company":
		return "web_company: research companies, products, funding, team info, and competitors."
	case "web_research":
		return "web_research: deep research on a topic using multiple sources."
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
