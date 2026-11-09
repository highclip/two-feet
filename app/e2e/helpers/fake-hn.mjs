// A local stand-in for the Hacker News search API, so a Playwright run never reaches the network.
// Every search gets the same stories; the app dedupes them.
import { createServer } from 'node:http';

const hits = [
  { title: 'Kubernetes gateway API reaches a new milestone', points: 310, author: 'somebody' },
  { title: 'Tail sampling in the OTel collector, explained', points: 220, author: 'somebody' },
  { title: 'Running Postgres on Kubernetes, five years in', points: 150, author: 'somebody' },
  { title: 'A cosy guide to sourdough starters', points: 900, author: 'somebody' },
];

createServer((_req, res) => {
  res.setHeader('Content-Type', 'application/json');
  res.end(JSON.stringify({ hits }));
}).listen(Number(process.env.FAKE_HN_PORT), '127.0.0.1');
