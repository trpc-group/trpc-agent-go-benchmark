# SWE-Bench Config

Shared benchmark configuration templates live here.

## Model Config

```bash
cp swebench/config/models/glm-5.2.yaml.example swebench/config/models/glm-5.2.local.yaml
```

For GLM-5.0 baseline runs:

```bash
cp swebench/config/models/glm-5.0.yaml.example swebench/config/models/glm-5.0.local.yaml
```

The committed MiniMax M2.5 baseline used this config name:

```bash
cp swebench/config/models/minimax-m2.5.yaml.example swebench/config/models/minimax-m2.5.local.yaml
```

Fill in the endpoint, API key, and required gateway headers in the local YAML.
Local config files are ignored by git and must not be committed.

## Environment Config

`environments/swebench-testbed.yaml` is committed and contains runtime settings
passed into SWE-Bench containers by mini-SWE-agent. It activates the official
`testbed` conda environment and limits common native-library thread counts.
