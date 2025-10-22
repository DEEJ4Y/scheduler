import { MongoClient } from 'mongodb';
import { MongoCron } from '..';

const NUM_CRONS = 100;
const NUM_JOBS = 10000;

interface ExecutionTracker {
  counts: Map<string, number>;
  timestamps: Map<string, number[]>;
}

function createTracker(): ExecutionTracker {
  return {
    counts: new Map(),
    timestamps: new Map(),
  };
}

function recordExecution(tracker: ExecutionTracker, jobId: string) {
  const count = tracker.counts.get(jobId) || 0;
  tracker.counts.set(jobId, count + 1);

  const times = tracker.timestamps.get(jobId) || [];
  times.push(Date.now());
  tracker.timestamps.set(jobId, times);
}

function pad(num: number, size: number): string {
  return String(num).padStart(size, '0');
}

function analyzeResults(tracker: ExecutionTracker, numJobs: number) {
  const stats = {
    totalExecutions: 0,
    uniqueJobs: tracker.counts.size,
    duplicateJobs: 0,
    totalDuplicates: 0,
    missedJobs: 0,
    duplicates: new Map<string, number>(),
    missedJobIds: [] as string[],
  };

  for (const [jobId, count] of tracker.counts.entries()) {
    stats.totalExecutions += count;
    if (count > 1) {
      stats.duplicateJobs++;
      stats.totalDuplicates += (count - 1);
      stats.duplicates.set(jobId, count);
    }
  }

  const executedSet = new Set(tracker.counts.keys());
  for (let i = 0; i < numJobs; i++) {
    const jobId = 'job-' + pad(i, 6);
    if (!executedSet.has(jobId)) {
      stats.missedJobIds.push(jobId);
    }
  }
  stats.missedJobs = stats.missedJobIds.length;

  return stats;
}

async function runConcurrencyTest() {
  console.log('================================================================================');
  console.log('NODEJS MONGODB-CRON CONCURRENCY TEST');
  console.log('================================================================================');
  console.log('Configuration: ' + NUM_CRONS + ' crons, ' + NUM_JOBS + ' jobs');
  console.log('');

  const client = await MongoClient.connect('mongodb://localhost:27017');
  const dbName = 'scheduler_nodejs_concurrency_' + Date.now();
  const testDb = client.db(dbName);
  const collection = testDb.collection('jobs');

  try {
    console.log('Creating index...');
    await collection.createIndex({ sleepUntil: 1 }, { sparse: true });

    console.log('Inserting ' + NUM_JOBS + ' jobs...');
    const insertStart = Date.now();

    const jobs = [];
    for (let i = 0; i < NUM_JOBS; i++) {
      jobs.push({
        jobID: 'job-' + pad(i, 6),
        sleepUntil: new Date(),
        data: 'test-job-' + i,
      });
    }

    const batchSize = 1000;
    for (let i = 0; i < jobs.length; i += batchSize) {
      const batch = jobs.slice(i, i + batchSize);
      await collection.insertMany(batch);
    }

    const insertDuration = Date.now() - insertStart;
    console.log('Inserted ' + NUM_JOBS + ' jobs in ' + insertDuration + 'ms');
    console.log('');

    const tracker = createTracker();
    let errorCount = 0;
    let stopping = false;

    console.log('Starting ' + NUM_CRONS + ' concurrent MongoCron instances...');
    const startTime = Date.now();

    const crons: MongoCron[] = [];
    const startPromises: Promise<void>[] = [];

    for (let i = 0; i < NUM_CRONS; i++) {
      const cronId = i;

      const cron = new MongoCron({
        collection,
        onDocument: async (doc) => {
          const jobId = doc.jobID || ('unknown-' + doc._id);
          recordExecution(tracker, jobId);
        },
        onError: (err) => {
          if (stopping && err.message && err.message.includes('cursor killed')) {
            return;
          }
          errorCount++;
          if (errorCount <= 5) {
            console.log('Cron ' + cronId + ' error:', err.message);
          }
        },
        nextDelay: 10,
        reprocessDelay: 0,
        idleDelay: 0,
        lockDuration: 30000,
      });

      crons.push(cron);
    }

    for (const cron of crons) {
      startPromises.push(cron.start());
    }
    await Promise.all(startPromises);

    const progressInterval = setInterval(() => {
      const completed = tracker.counts.size;
      const executions = Array.from(tracker.counts.values()).reduce((sum, count) => sum + count, 0);
      console.log('Progress: ' + executions + ' executions, ' + completed + ' unique jobs');
    }, 5000);

    let allProcessed = false;
    let lastProgress = 0;
    let idleStartTime = Date.now();
    const maxIdleTime = 10000;

    while (!allProcessed) {
      await new Promise(resolve => setTimeout(resolve, 2000));

      const completed = tracker.counts.size;
      const remaining = await collection.countDocuments({ sleepUntil: { $ne: null } });

      if (completed !== lastProgress) {
        lastProgress = completed;
        idleStartTime = Date.now();
      }

      if (remaining === 0 && completed >= NUM_JOBS) {
        allProcessed = true;
      } else if (Date.now() - idleStartTime > maxIdleTime && remaining === 0) {
        allProcessed = true;
      }
    }

    clearInterval(progressInterval);
    const duration = Date.now() - startTime;

    console.log('Stopping all crons...');
    stopping = true;
    const stopPromises = crons.map(cron => cron.stop());
    await Promise.all(stopPromises);

    console.log('');
    console.log('================================================================================');
    console.log('TEST RESULTS');
    console.log('================================================================================');

    const stats = analyzeResults(tracker, NUM_JOBS);

    console.log('');
    console.log('Test Configuration:');
    console.log('  Concurrent crons:         ' + NUM_CRONS);
    console.log('  Total jobs queued:        ' + NUM_JOBS);

    console.log('');
    console.log('Execution Statistics:');
    console.log('  Total executions:         ' + stats.totalExecutions);
    console.log('  Unique jobs executed:     ' + stats.uniqueJobs);
    console.log('  Jobs with duplicates:     ' + stats.duplicateJobs);
    console.log('  Total duplicate runs:     ' + stats.totalDuplicates);
    console.log('  Jobs not executed:        ' + stats.missedJobs);
    console.log('  Errors encountered:       ' + errorCount);

    console.log('');
    console.log('Performance Metrics:');
    console.log('  Total duration:           ' + (duration / 1000).toFixed(3) + 's');
    console.log('  Jobs per second:          ' + (stats.totalExecutions / (duration / 1000)).toFixed(2));
    console.log('  Avg time per job:         ' + (duration * 1000 / stats.totalExecutions).toFixed(0) + 'us');
    console.log('  Throughput per cron:      ' + (stats.totalExecutions / (duration / 1000) / NUM_CRONS).toFixed(2) + ' jobs/sec');

    if (stats.duplicates.size > 0) {
      console.log('');
      console.log('WARNING: Duplicate Executions Detected!');
      console.log('Showing first 5 examples:');

      let count = 0;
      for (const [jobId, execCount] of stats.duplicates.entries()) {
        if (count < 5) {
          const times = tracker.timestamps.get(jobId) || [];
          console.log('');
          console.log('  Job ' + jobId + ': executed ' + execCount + ' times');
          times.forEach((time, i) => {
            console.log('    Execution ' + (i + 1) + ': ' + new Date(time).toISOString());
            if (i > 0) {
              const gap = time - times[i - 1];
              console.log('      Time gap: ' + gap + 'ms');
            }
          });
        }
        count++;
      }

      if (count > 5) {
        console.log('  ... and ' + (count - 5) + ' more jobs with duplicates');
      }
    }

    if (stats.missedJobIds.length > 0) {
      console.log('');
      console.log('Missed Jobs:');
      stats.missedJobIds.slice(0, 10).forEach(jobId => {
        console.log('  ' + jobId);
      });
      if (stats.missedJobIds.length > 10) {
        console.log('  ... and ' + (stats.missedJobIds.length - 10) + ' more missed jobs');
      }
    }

    console.log('');
    console.log('================================================================================');

    if (stats.totalDuplicates > 0) {
      console.log('FAILED: Found ' + stats.totalDuplicates + ' duplicate executions across ' + stats.duplicateJobs + ' jobs');
      process.exit(1);
    }

    if (stats.missedJobs > 0) {
      console.log('FAILED: ' + stats.missedJobs + ' jobs were not executed');
      process.exit(1);
    }

    if (stats.uniqueJobs !== NUM_JOBS) {
      console.log('FAILED: Expected ' + NUM_JOBS + ' unique jobs executed, got ' + stats.uniqueJobs);
      process.exit(1);
    }

    console.log('SUCCESS: All ' + NUM_JOBS + ' jobs executed exactly once!');
    console.log('NO DUPLICATES found with ' + NUM_CRONS + ' concurrent crons!');
    console.log('Distributed locking mechanism PASSED stress test!');
    console.log('Achieved ' + (stats.totalExecutions / (duration / 1000)).toFixed(0) + ' jobs/second throughput');
    console.log('');

  } finally {
    await testDb.dropDatabase();
    await client.close();
  }
}

runConcurrencyTest().catch((error) => {
  console.error('Test failed with error:', error);
  process.exit(1);
});
