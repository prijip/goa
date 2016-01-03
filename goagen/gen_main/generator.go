package genmain

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"unicode"

	"github.com/raphael/goa/design"
	"github.com/raphael/goa/goagen/codegen"
	"github.com/raphael/goa/goagen/utils"

	"gopkg.in/alecthomas/kingpin.v2"
)

// Generator is the application code generator.
type Generator struct {
	genfiles []string
}

// Generate is the generator entry point called by the meta generator.
func Generate(api *design.APIDefinition) ([]string, error) {
	g, err := NewGenerator()
	if err != nil {
		return nil, err
	}
	return g.Generate(api)
}

// NewGenerator returns the application code generator.
func NewGenerator() (*Generator, error) {
	app := kingpin.New("Main generator", "application main generator")
	codegen.RegisterFlags(app)
	NewCommand().RegisterFlags(app)
	_, err := app.Parse(os.Args[1:])
	if err != nil {
		return nil, fmt.Errorf(`invalid command line: %s. Command line was "%s"`,
			err, strings.Join(os.Args, " "))
	}
	return new(Generator), nil
}

// controllerVersion is the data structure used to render a specific version of the controller
// mounting code.
type controllerVersion struct {
	Controller *design.ResourceDefinition
	Version    string
}

func newControllerVersion(ctrl *design.ResourceDefinition, version string) *controllerVersion {
	return &controllerVersion{
		Controller: ctrl,
		Version:    version,
	}
}

// Generate produces the skeleton main.
func (g *Generator) Generate(api *design.APIDefinition) (_ []string, err error) {
	go utils.Catch(nil, func() { g.Cleanup() })

	defer func() {
		if err != nil {
			g.Cleanup()
		}
	}()

	mainFile := filepath.Join(codegen.OutputDir, "main.go")
	if Force {
		os.Remove(mainFile)
	}
	g.genfiles = append(g.genfiles, mainFile)
	_, err = os.Stat(mainFile)
	funcs := template.FuncMap{
		"tempvar":              tempvar,
		"generateSwagger":      generateSwagger,
		"goify":                codegen.Goify,
		"okResp":               okResp,
		"newControllerVersion": newControllerVersion,
	}
	gopath := filepath.SplitList(os.Getenv("GOPATH"))[0]
	if err != nil {
		var tmpl *template.Template
		tmpl, err = template.New("main").Funcs(funcs).Parse(mainTmpl)
		if err != nil {
			panic(err.Error()) // bug
		}
		gg := codegen.NewGoGenerator(mainFile)
		var outPkg string
		outPkg, err = filepath.Rel(gopath, codegen.OutputDir)
		if err != nil {
			return
		}
		outPkg = strings.TrimPrefix(outPkg, "src/")
		appPkg := filepath.Join(outPkg, "app")
		swaggerPkg := filepath.Join(outPkg, "swagger")
		imports := []*codegen.ImportSpec{
			codegen.SimpleImport("github.com/raphael/goa"),
			codegen.SimpleImport(appPkg),
			codegen.SimpleImport(swaggerPkg),
			codegen.NewImport("log", "gopkg.in/inconshreveable/log15.v2"),
		}
		if generateSwagger() {
			jsonSchemaPkg := filepath.Join(outPkg, "schema")
			imports = append(imports, codegen.SimpleImport(jsonSchemaPkg))
		}
		gg.WriteHeader("", "main", imports)
		data := map[string]interface{}{
			"Name": AppName,
			"API":  api,
		}
		if err = tmpl.Execute(gg, data); err != nil {
			return
		}
		if err = gg.FormatCode(); err != nil {
			return
		}
	}
	tmpl, err := template.New("ctrl").Funcs(funcs).Parse(ctrlTmpl)
	if err != nil {
		panic(err.Error()) // bug
	}
	imp, err := filepath.Rel(filepath.Join(gopath, "src"), codegen.OutputDir)
	if err != nil {
		return
	}
	imp = filepath.Join(imp, "app")
	imports := []*codegen.ImportSpec{codegen.SimpleImport(imp)}
	err = api.IterateResources(func(r *design.ResourceDefinition) error {
		filename := filepath.Join(codegen.OutputDir, snakeCase(r.Name)+".go")
		if Force {
			if err := os.Remove(filename); err != nil {
				return err
			}
		}
		g.genfiles = append(g.genfiles, filename)
		if _, err := os.Stat(filename); err != nil {
			resGen := codegen.NewGoGenerator(filename)
			resGen.WriteHeader("", "main", imports)
			err := tmpl.Execute(resGen, r)
			if err != nil {
				return err
			}
			if err := resGen.FormatCode(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return
	}

	return g.genfiles, nil
}

// Cleanup removes all the files generated by this generator during the last invokation of Generate.
func (g *Generator) Cleanup() {
	for _, f := range g.genfiles {
		os.Remove(f)
	}
	g.genfiles = nil
}

// tempCount is the counter used to create unique temporary variable names.
var tempCount int

// tempvar generates a unique temp var name.
func tempvar() string {
	tempCount++
	if tempCount == 1 {
		return "c"
	}
	return fmt.Sprintf("c%d", tempCount)
}

// generateSwagger returns true if the API Swagger spec should be generated.
func generateSwagger() bool {
	return codegen.CommandName == "" || codegen.CommandName == "swagger"
}

func okResp(a *design.ActionDefinition) map[string]interface{} {
	var ok *design.ResponseDefinition
	for _, resp := range a.Responses {
		if resp.Status == 200 {
			ok = resp
			break
		}
	}
	if ok == nil {
		return nil
	}
	var mt *design.MediaTypeDefinition
	var ok2 bool
	if mt, ok2 = design.Design.MediaTypes[ok.MediaType]; !ok2 {
		return nil
	}
	typeref := codegen.GoTypeRef(mt, 1)
	if strings.HasPrefix(typeref, "*") {
		typeref = "&app." + typeref[1:]
	} else {
		typeref = "app." + typeref
	}
	return map[string]interface{}{
		"Name":             ok.Name,
		"HasMultipleViews": len(mt.Views) > 1,
		"GoType":           codegen.GoNativeType(mt),
		"TypeRef":          typeref,
	}
}

// snakeCase produces the snake_case version of the given CamelCase string.
func snakeCase(name string) string {
	var b bytes.Buffer
	var lastUnderscore bool
	ln := len(name)
	if ln == 0 {
		return ""
	}
	b.WriteRune(unicode.ToLower(rune(name[0])))
	for i := 1; i < ln; i++ {
		r := rune(name[i])
		if unicode.IsUpper(r) {
			if !lastUnderscore {
				b.WriteRune('_')
				lastUnderscore = true
			}
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
			lastUnderscore = false
		}
	}
	return b.String()
}

const mainTmpl = `
func main() {
	// Create service
	service := goa.New("{{.Name}}")

	// Setup middleware
	service.Use(goa.RequestID())
	service.Use(goa.LogRequest())
	service.Use(goa.Recover())
{{$api := .API}}
{{range $name, $res := $api.Resources}}{{if $res.SupportsNoVersion}}{{$name := goify $res.Name true}}	// Mount "{{$res.Name}}" controller
	{{$tmp := tempvar}}{{$tmp}} := New{{$name}}Controller(service)
	app.Mount{{$name}}Controller(service, {{$tmp}})
{{end}}{{end}}{{range $ver, $prop := $api.Versions}}
	// Version {{$ver}}
{{range $name, $res := $api.Resources}}{{if $res.SupportsVersion $ver}}{{$name := goify (printf "%s%s" $res.Name $ver) true}}	// Mount "{{$res.Name}}" controller
	{{$tmp := tempvar}}{{$tmp}} := New{{$name}}Controller(service)
	{{goify $ver false}}.Mount{{goify $res.Name true}}Controller(service, {{$tmp}})
{{end}}{{end}}
{{end}}{{if generateSwagger}}// Mount Swagger spec provider controller
	swagger.MountController(service)
{{end}}
	// Start service, listen on port 8080
	service.ListenAndServe(":8080")
}
`
const ctrlTmpl = `{{define "OneVersion"}}` + ctrlVerTmpl + `{{end}}` + `{{$ctrl := .}}{{/*
*/}}{{if .APIVersions}}{{range $ver := .APIVersions}}{{template "OneVersion" (newControllerVersion $ctrl $ver)}}
{{end}}{{else}}{{template "OneVersion" (newControllerVersion $ctrl "")}}
{{end}}`

const ctrlVerTmpl = `// {{$ctrlName := printf "%s%s" (goify (printf "%s%s" .Controller.Name .Version)  true) "Controller"}}{{$ctrlName}} implements the{{if .Version}} {{.Version}} {{end}}{{.Controller.Name}} resource.
type {{$ctrlName}} struct {
	goa.Controller
}

// New{{$ctrlName}} creates a {{.Controller.Name}} controller.
func New{{$ctrlName}}(service goa.Service) {{if .Version}}{{goify .Version false}}{{else}}app{{end}}.{{$ctrlName}} {
	return &{{$ctrlName}}{Controller: service.NewController("{{$ctrlName}}")}
}
{{$ctrl := .Controller}}{{range .Controller.Actions}}
// {{goify .Name true}} runs the {{.Name}} action.
func (c *{{$ctrlName}}) {{goify .Name true}}(ctx *app.{{goify .Name true}}{{goify $ctrl.Name true}}Context) error {
{{$ok := okResp .}}{{if $ok}}	res := {{$ok.TypeRef}}{}
{{end}}	return {{if $ok}}ctx.{{$ok.Name}}(res{{if $ok.HasMultipleViews}}, "default"{{end}}){{else}}nil{{end}}
}
{{end}}
`
