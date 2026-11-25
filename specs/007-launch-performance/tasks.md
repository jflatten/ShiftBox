# Launch performance logging

- [x] T001 Persist and read the launch workflow milestones, provider timings, guest timings and image-cache summary in SQLite and PostgreSQL.
- [x] T002 Collect provider and guest phase timings and measure registry traffic during launch. Validate guest authority, bounded input, and retries.
- [x] T003 Show live and historical launch timings to workspace owners and authorized admins. Verify access and persistence after reload.
- [x] T004 Add administrator log retention with a 30-day default, validation, audit and cleanup every four hours.
- [x] T005 Validate the launch workflow on shiftbox-demo and report the review URL, running revision, and checks.
- [x] T006 Group launch sub-timings beneath their controller steps and verify the hierarchy on the demo.
- [x] T007 Reconcile each displayed step breakdown to its elapsed time and label diagnostic measurements that overlap.
- [x] T008 Capture and attribute Dev Container configuration substeps and verify the breakdown on shiftbox-demo.
- [x] T009 Organize workspace details into Carbon-style tabs and verify the layout on shiftbox-demo.
- [x] T010 Show fresh timings for the latest workspace launch or start. Verify the new operation replaces earlier timings immediately and after reload, and that timing samples stay scoped to their operation.
- [x] T011 Deploy the controller fix to `rc3` and verify a live `groundtruth` stop and start. Revision 17 cleared the previous totals, recorded 47.390 seconds to IDE readiness and 48.037 seconds to setup completion, and retained both after reload.
- [ ] T012 Install the updated agent on the retained `groundtruth` VM and verify detailed guest timings. The unsigned build requires the signature trusted by the public RC3 AMI.
