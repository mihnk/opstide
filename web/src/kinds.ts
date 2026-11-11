// The kinds OpsTide watches, by the collector's name for each watch. Kubernetes kind names stay in English in every
// language; only the words around them are translated.
export const kindName: Record<string, string> = { cronjobs: 'CronJobs', jobs: 'Jobs', pods: 'Pods', deployments: 'Deployments', replicasets: 'ReplicaSets', deploypods: 'Deployment Pods', events: 'Kubernetes Events', hpas: 'HorizontalPodAutoscalers', statefulsets: 'StatefulSets', daemonsets: 'DaemonSets', controllerrevisions: 'ControllerRevisions', workloadpods: 'StatefulSet and DaemonSet Pods', nodes: 'Nodes' }
