---
name: Feature request
about: Propose a primitive, a step, or a change to how one behaves
title: ''
labels: enhancement
assignees: ''
---

**What are you trying to do**

<!-- The task, not the feature. The most useful requests describe a deploy that is awkward today. -->

**What you do today instead**

<!-- The workaround. If you are shelling out around a package, say which one — that is usually the real report. -->

**Mechanism or decision?**

<!-- The question that decides whether this belongs in lath at all.

A MECHANISM is how something is done: write atomically, poll until ready, remove an image. A DECISION is what someone chose: which version counts as "previous", that a tag is named after a binary, how many releases to keep.

lath ships mechanisms; projects compose them into decisions in their own definition. If two projects would want this to mean different things, it is probably a decision — and the request is more likely "expose the pieces" than "add the verb". Say which you think it is; being wrong is fine, the answer is often interesting. -->

**Would you use the pieces, if the whole were not built?**

<!-- e.g. "I'd write the step myself if kit could just remove an image." Answering yes usually means a much smaller change ships much sooner. -->
