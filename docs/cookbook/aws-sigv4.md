# Cookbook: AWS SigV4

`aws_sigv4` is a built-in message-layer provider. It signs the request after
interpolation and after the body is final (including uploads), then adds
`Authorization`, `X-Amz-Date`, and `X-Amz-Content-Sha256`. A retry signs
again. Field reference and the environment-variable fallback are in
`docs/auth.md`.

```yaml
auth:
  - name: aws
    type: aws_sigv4
    config:
      # Omit these three to read AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY,
      # and AWS_SESSION_TOKEN. There is no profile or credentials file.
      accessKeyId: "${env:AWS_ACCESS_KEY_ID}"
      secretAccessKey: "${env:AWS_SECRET_ACCESS_KEY}"
      sessionToken: "${env:AWS_SESSION_TOKEN}"
      region: "${AWS_REGION}"
      service: execute-api
suites:
  - name: API
    auth: { use: [aws] }
    cases:
      - name: signed PUT
        steps:
          - name: put item
            protocol: rest
            request:
              method: PUT
              url: "${BASE_URL}/items"
              json: { name: "widget" }
            assertions:
              - type: status
                value: 200
```

`service: s3` keeps a single URI encoding and does not normalize the path.
Other services encode an already-percent-encoded octet again (`%20` becomes
`%2520`) and remove dot segments. Put `aws_sigv4` last in `auth.use` when
another message provider adds headers that must be signed.

Generic HMAC for webhooks is `type: hmac`, documented in `docs/auth.md`.
NTLM/Kerberos is not built in.
