package proxy

// nssNeeded is whether a browser here keeps its own NSS store: none on
// Windows, where Firefox 120 and later read the roots a user adds to the
// Windows store.
const nssNeeded = false
