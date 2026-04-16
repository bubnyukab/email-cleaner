'use client';

import {
  analyticsLabels,
  analyticsTimeline,
  analyticsTopSenders,
  type AnalyticsLabelEntry,
  type AnalyticsTimelineEntry,
  type AnalyticsTopSender,
} from '@/lib/go/client';
import { useEffect, useState } from 'react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Line,
  LineChart,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';

// CSS custom properties already include the full hsl() value — do NOT wrap with hsl() again.
const CHART_COLORS = [
  'var(--chart-1)',
  'var(--chart-2)',
  'var(--chart-3)',
  'var(--chart-4)',
  'var(--chart-5)',
];

const LABEL_MAP: Record<string, string> = {
  INBOX: 'Inbox',
  CATEGORY_PERSONAL: 'Personal',
  CATEGORY_SOCIAL: 'Social',
  CATEGORY_PROMOTIONS: 'Promotions',
  CATEGORY_UPDATES: 'Updates',
  CATEGORY_FORUMS: 'Forums',
  SPAM: 'Spam',
  TRASH: 'Trash',
  UNREAD: 'Unread',
  SENT: 'Sent',
  STARRED: 'Starred',
  IMPORTANT: 'Important',
};

function labelName(label: string): string {
  return LABEL_MAP[label] ?? label;
}

function formatDay(v: string): string {
  const d = new Date(v);
  return `${d.getUTCMonth() + 1}/${d.getUTCDate()}`;
}

export function AnalyticsDashboard() {
  const [topSenders, setTopSenders] = useState<AnalyticsTopSender[]>([]);
  const [timeline, setTimeline] = useState<AnalyticsTimelineEntry[]>([]);
  const [labels, setLabels] = useState<AnalyticsLabelEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [open, setOpen] = useState(false);

  useEffect(() => {
    if (!open) return;
    setLoading(true);
    Promise.all([analyticsTopSenders(), analyticsTimeline(), analyticsLabels()])
      .then(([ts, tl, lb]) => {
        setTopSenders(ts);
        setTimeline(tl);
        setLabels(lb.slice(0, 8));
      })
      .catch(() => {})
      .finally(() => setLoading(false));
  }, [open]);

  return (
    <div className="mt-6 rounded-lg border border-border">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center justify-between px-4 py-3 text-left text-sm font-semibold hover:bg-accent"
      >
        <span>Analytics</span>
        <span className="text-muted-foreground">{open ? '▲' : '▼'}</span>
      </button>

      {open && (
        <div className="border-t border-border p-4 pb-6">
          {loading ? (
            <p className="py-12 text-center text-sm text-muted-foreground">Loading analytics…</p>
          ) : (
            <div className="space-y-8">
              {/* Row 1: Bar + Pie side by side on large screens */}
              <div className="grid grid-cols-1 gap-8 lg:grid-cols-5">
                {/* Top Senders Bar Chart — takes 3/5 width on lg */}
                <div className="lg:col-span-3">
                  <h3 className="mb-4 text-sm font-semibold text-foreground">
                    Top 10 Senders by Volume
                  </h3>
                  {topSenders.length === 0 ? (
                    <p className="py-8 text-center text-sm text-muted-foreground">
                      No data available.
                    </p>
                  ) : (
                    <ResponsiveContainer width="100%" height={320}>
                      <BarChart
                        data={topSenders}
                        layout="vertical"
                        margin={{ left: 0, right: 32, top: 4, bottom: 4 }}
                      >
                        <CartesianGrid strokeDasharray="3 3" horizontal={false} stroke="var(--border)" />
                        <XAxis
                          type="number"
                          tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
                          axisLine={false}
                          tickLine={false}
                        />
                        <YAxis
                          type="category"
                          dataKey="name"
                          width={140}
                          tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
                          tickFormatter={(v: string) => (v.length > 20 ? v.slice(0, 18) + '…' : v)}
                          axisLine={false}
                          tickLine={false}
                        />
                        <Tooltip
                          formatter={(v) => [`${v} emails`, 'Count']}
                          contentStyle={{
                            fontSize: 12,
                            background: 'var(--popover)',
                            border: '1px solid var(--border)',
                            borderRadius: 6,
                          }}
                        />
                        <Bar dataKey="count" fill={CHART_COLORS[0]} radius={[0, 4, 4, 0]} maxBarSize={24} />
                      </BarChart>
                    </ResponsiveContainer>
                  )}
                </div>

                {/* Email Categories Pie/Donut — takes 2/5 width on lg */}
                <div className="lg:col-span-2">
                  <h3 className="mb-4 text-sm font-semibold text-foreground">Email Categories</h3>
                  {labels.length === 0 ? (
                    <p className="py-8 text-center text-sm text-muted-foreground">
                      No label data available.
                    </p>
                  ) : (
                    <ResponsiveContainer width="100%" height={320}>
                      <PieChart margin={{ top: 8, right: 8, bottom: 8, left: 8 }}>
                        <Pie
                          data={labels}
                          dataKey="count"
                          nameKey="label"
                          cx="50%"
                          cy="40%"
                          innerRadius={52}
                          outerRadius={88}
                          paddingAngle={2}
                        >
                          {labels.map((entry, idx) => (
                            <Cell
                              key={entry.label}
                              fill={CHART_COLORS[idx % CHART_COLORS.length]}
                            />
                          ))}
                        </Pie>
                        <Tooltip
                          formatter={(v, _, p) => [
                            `${v} emails`,
                            labelName(p.payload.label as string),
                          ]}
                          contentStyle={{
                            fontSize: 12,
                            background: 'var(--popover)',
                            border: '1px solid var(--border)',
                            borderRadius: 6,
                          }}
                        />
                        <Legend
                          layout="horizontal"
                          align="center"
                          verticalAlign="bottom"
                          iconSize={10}
                          iconType="circle"
                          formatter={(value) => labelName(value as string)}
                          wrapperStyle={{ fontSize: 11, lineHeight: '20px' }}
                        />
                      </PieChart>
                    </ResponsiveContainer>
                  )}
                </div>
              </div>

              {/* Row 2: Timeline — full width */}
              <div>
                <h3 className="mb-4 text-sm font-semibold text-foreground">
                  Emails Received (last 6 months)
                </h3>
                {timeline.length === 0 ? (
                  <p className="py-8 text-center text-sm text-muted-foreground">
                    No timeline data available.
                  </p>
                ) : (
                  <ResponsiveContainer width="100%" height={240}>
                    <LineChart
                      data={timeline}
                      margin={{ left: 0, right: 16, top: 4, bottom: 4 }}
                    >
                      <CartesianGrid strokeDasharray="3 3" stroke="var(--border)" />
                      <XAxis
                        dataKey="day"
                        tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
                        tickFormatter={formatDay}
                        interval="preserveStartEnd"
                        axisLine={false}
                        tickLine={false}
                        minTickGap={40}
                      />
                      <YAxis
                        tick={{ fontSize: 11, fill: 'var(--muted-foreground)' }}
                        axisLine={false}
                        tickLine={false}
                        width={36}
                      />
                      <Tooltip
                        labelFormatter={(label: string) =>
                          new Date(label).toLocaleDateString('en-US', {
                            timeZone: 'UTC',
                            month: 'short',
                            day: 'numeric',
                            year: 'numeric',
                          })
                        }
                        formatter={(v) => [`${v} emails`, 'Count']}
                        contentStyle={{
                          fontSize: 12,
                          background: 'var(--popover)',
                          border: '1px solid var(--border)',
                          borderRadius: 6,
                        }}
                      />
                      <Line
                        type="monotone"
                        dataKey="count"
                        stroke={CHART_COLORS[1]}
                        dot={false}
                        strokeWidth={2}
                        activeDot={{ r: 4, fill: CHART_COLORS[1] }}
                      />
                    </LineChart>
                  </ResponsiveContainer>
                )}
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
