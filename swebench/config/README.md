# SWE-Bench Configuration

Only templates belong in this directory. Local model credentials and endpoint
details must be stored in ignored `*.local.yaml` files.

## Model configuration

```bash
cp swebench/config/models/openai-compatible.yaml.example \
  swebench/config/models/openai-compatible.local.yaml
```

The loader understands the model subset used by mini-SWE-agent, including
arbitrary `model.model_kwargs.extra_headers`. Secret-looking values are
redacted from committed manifests and command logs.

## Environment configuration

`environments/swebench-testbed.yaml` activates the official testbed Conda
environment and limits common native-library thread pools for the external
mini-SWE-agent runner.
