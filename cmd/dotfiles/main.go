package main

import (
	"os"

	dotfiles "github.com/amichaelyu/dotfiles"
	"github.com/amichaelyu/dotfiles/internal/app"
)

func main() {
	app.Template = dotfiles.Template
	os.Exit(app.Main(os.Args[1:]))
}
