{{- define "twofeet.image" -}}
{{ .Values.image.repository }}:{{ required "image.tag is required (CI sets it; manual deploys use IMAGE_TAG)" .Values.image.tag }}
{{- end -}}

{{- define "twofeet.env" -}}
- name: PORT
  value: {{ .Values.port | quote }}
- name: PUBLIC_URL
  value: https://{{ .Values.publicHost }}
- name: EVENT_SLUG
  value: {{ .Values.eventSlug | quote }}
- name: OTEL_SERVICE_NAME
  value: two-feet
- name: OTEL_RESOURCE_ATTRIBUTES
  value: service.version={{ .Values.image.tag }},deployment.environment=hackathon,k8s.cluster.name={{ .Values.cluster.name }}
- name: OTEL_EXPORTER_OTLP_ENDPOINT
  value: https://{{ .Values.dash0.endpoint }}
- name: DASH0_AUTH_TOKEN
  valueFrom:
    secretKeyRef: {name: two-feet-env, key: DASH0_AUTH_TOKEN}
- name: OTEL_EXPORTER_OTLP_HEADERS
  value: Authorization=Bearer%20$(DASH0_AUTH_TOKEN),Dash0-Dataset={{ .Values.dash0.dataset }}
{{- range list "DATABASE_URL" "ELASTIC_URL" "ELASTIC_API_KEY" "CLERK_PUBLISHABLE_KEY" "CLERK_SECRET_KEY" }}
- name: {{ . }}
  valueFrom:
    secretKeyRef: {name: two-feet-env, key: {{ . }}}
{{- end }}
{{- end -}}
