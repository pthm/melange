{{- .Title | replaceRE "\n" " " | printf "# %s" }}
{{ with .Description }}
> {{ . }}
{{ end }}
{{ partial "llms/markdown-body.html" . }}
