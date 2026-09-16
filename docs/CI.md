# CI and Docker releases

The workflows follow [Stitch's release setup](https://github.com/InjectiveLabs/stitch/blob/master/.github/workflows/release.yaml): Blacksmith runners, a cached Docker builder, and Amazon ECR Public with GitHub OIDC authentication.

## Checks

[CI](../.github/workflows/ci.yml) runs on pull requests, pushes to `master` or `main`, and manual dispatches. Releases call the same checks before publishing. The `blacksmith-8vcpu-ubuntu-2404` job runs:

- Go race tests before generating the web bundle.
- Svelte/TypeScript checks, frontend tests, and a production web build.
- Go vet, race tests with the embedded web UI, and a binary build.
- A Docker build and Compose smoke test of the health endpoint, embedded UI, and metrics.

Dependencies use the committed lockfiles; workflow actions are pinned to commits. Blacksmith runner labels are listed in [.github/actionlint.yaml](../.github/actionlint.yaml) for local workflow validation with `actionlint`.

## Publish an image

[Release Docker image](../.github/workflows/release.yml) publishes `linux/amd64` images on `blacksmith-32vcpu-ubuntu-2404` to:

```text
public.ecr.aws/l9h3g6c6/cmt-top
```

Choose a new version and push its tag after the code is ready. For example, a tag named `v1.2.3` publishes `:v1.2.3` and updates `:latest`. A prerelease such as `v1.2.3-rc.1` publishes only that version tag. These are examples, not the current release version.

The migration preserves the historical `v0.1.0` and `v0.1.1` tags, including their original workflow files. Their release jobs were not rerun during import. ECR images begin with a new version tag on the migrated code; use the [local Docker build](USAGE.md#build) until that first release.

For a retry, use **Actions → Release Docker image → Run workflow** and select the version tag, or run `gh workflow run release.yml --ref <version-tag>`. A branch dispatch is rejected. Publishing a GitHub Release alone does not start another build; this avoids duplicate image pushes for the same tag.

The workflow requires the GitHub repository variable `AWS_ROLE_ARN` set to `arn:aws:iam::981432137740:role/cmt-top`. It does not require Docker Hub credentials or long-lived AWS keys. Blacksmith's GitHub installation must include this repository.

## AWS publishing access

The ECR repository and IAM role use the same AWS account and public registry as Stitch, with a separate role for cmt-top. The checked-in policies describe that role:

- [Trust policy](../deploy/ci/aws-trust-policy.json): accepts GitHub OIDC tokens only for this repository's `v*` tag refs, with the `sts.amazonaws.com` audience. It uses GitHub's immutable owner and repository IDs returned by the repository's OIDC settings.
- [Push policy](../deploy/ci/aws-push-policy.json): grants ECR Public authentication and image upload only to the `cmt-top` repository. It grants no repository deletion, IAM administration, or access to Stitch images.

These are AWS configuration records; editing them does not automatically change IAM. If the repository is recreated or its OIDC subject settings change, update and apply the trust policy before the next release. Branch and pull-request jobs cannot assume this publishing role.

See [GitHub's OIDC reference](https://docs.github.com/en/actions/reference/security/oidc) and [AWS's ECR Public permissions reference](https://docs.aws.amazon.com/AmazonECR/latest/public/public-repository-policy-examples.html) for the subject format and required upload permissions.
