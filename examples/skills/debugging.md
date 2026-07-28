---
name: Debugging
description: Systematically identify and fix software defects.
---

# Goal

Find the root cause of a bug instead of treating symptoms.

# Process

1. Reproduce the issue.
   - Identify exact steps.
   - Verify it happens consistently.

2. Gather evidence.
   - Read stack traces.
   - Check logs.
   - Inspect recent changes.
   - Compare expected vs actual behavior.

3. Form hypotheses.
   - Write down the most likely causes.
   - Test one hypothesis at a time.

4. Minimize the problem.
   - Create the smallest reproducible example.
   - Remove unrelated code.

5. Fix the root cause.
   - Avoid hacks.
   - Keep the change as small as possible.

6. Verify.
   - Confirm the original bug is fixed.
   - Check for regressions.
   - Run relevant tests.

# Rules

- Never guess.
- Don't make multiple unrelated changes at once.
- Explain why the bug happened.
- Prefer evidence over intuition.

# Checklist

- [ ] Bug reproduced
- [ ] Root cause identified
- [ ] Fix implemented
- [ ] Tests pass
- [ ] No regressions found