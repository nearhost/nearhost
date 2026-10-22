//go:build unix && !linux

package engine

// holderTools are the tools PortHolder asks, in order.
func holderTools() []holderTool { return []holderTool{lsofTool} }
