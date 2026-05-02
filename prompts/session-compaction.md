이전 세션 요약이에요.
보존하지 않는 오래된 turn만 압축했어요.
{{- range .Turns }}
- 사용자 요청: {{ index . "Prompt" }}
{{- with index . "Response" }}
  응답 요약: {{ . }}
{{- end }}
{{- with index . "Error" }}
  오류: {{ . }}
{{- end }}
{{- end }}
