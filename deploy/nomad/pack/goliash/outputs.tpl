Goliash deployed as job "[[ var "job_name" . ]]" (host-networked on port [[ var "port" . ]]).

UI and API:  [[ if ne (var "public_url" .) "" ]][[ var "public_url" . ]][[ else ]]http://<node-ip>:[[ var "port" . ]][[ end ]]
Discovery:   Nomad service "[[ var "job_name" . ]]" (provider=nomad)

First sign-in: open the task logs and use the link logged for [[ var "owner_email" . ]]
("bootstrap: sign in as the owner"). It works once and expires in 15 minutes; restart the task for a new one.
[[ if var "watch_nomad" . ]]
The Nomad cluster is watched as target "[[ var "job_name" . ]]-nomad" in environment "[[ var "environment" . ]]".
Workloads labelled with goliash.service in job meta map to services by themselves; the rest wait in the Inbox.
[[ end ]]
The SQLite database and the secret key live on the [[ var "data_volume" . ]] volume (/data) — back both up, and
pin the job to that node with the constraints variable. Put it behind TLS before exposing it.
