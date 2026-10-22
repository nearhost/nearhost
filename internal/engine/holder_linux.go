package engine

// holderTools are the tools PortHolder asks, in order: ss is installed on
// more Linux systems than lsof.
func holderTools() []holderTool { return []holderTool{ssTool, lsofTool} }
