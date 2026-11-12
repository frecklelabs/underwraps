package web

import _ "embed"

//go:embed dist/index.html
var Index string

//go:embed dist/app.js
var JS string

//go:embed dist/app.css
var CSS string
