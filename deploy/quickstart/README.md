# Praxis alpha quick start

This bundle runs the published Praxis alpha images with Restate and Moto, a
local AWS-compatible API. It does not compile Praxis or require a repository
clone.

Prerequisite: Docker with Docker Compose.

1. Start and register the complete stack:

   ```sh
   ./praxis-up
   ```

2. Install the `praxis` CLI from the matching GitHub alpha release and verify
   the stack:

   ```sh
   praxis version
   praxis list schemas
   ```

3. Plan and deploy the included S3 example:

   ```sh
   praxis plan bucket.cue --account local
   praxis deploy bucket.cue --account local --key quickstart --yes --wait
   praxis get Deployment/quickstart
   ```

4. Remove the example and stop the stack:

   ```sh
   praxis delete Deployment/quickstart --yes --wait
   ./praxis-down
   ```

The `alpha` release is one mutable contract. Update the CLI and all service
images together. Alpha revisions may break existing state and templates.
