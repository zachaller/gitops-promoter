//HARDCODED:  GETS DRY/HYDRATED COMMIT DETAILS 
async function getCommitEnrichment(owner: string, repoName: string, sha: string): Promise<{ author: string, message: string, url: string }> {
  let author = 'Shirley Huang';
  let message = 'Update kustomization.yaml';
  let url = '';
  // if (owner && repoName && sha) {
  //   url = `https://github.com/${owner}/${repoName}/commit/${sha}`;
  //   try {
  //     // const resp = await fetch(`https://api.github.com/repos/${owner}/${repoName}/commits/${sha}`);
  //     // if (resp.ok) {
  //     //   const data = await resp.json();
  //     //   author = data?.commit?.author?.name || data?.author?.login || '-';
  //     //   message = data?.commit?.message || '-';
  //     //   console.log(author,message);
  //     // }
  //   } catch (e) {
  //     author = '-';
  //     message = '-';
  //     url = '';
  //   }
  // }
  return { author, message, url };
}

//HARDCODED: Get PR number (GITHUB API)
async function getPRNumberFromCommit(owner: string, repo: string, sha: string): Promise<number | null> {
  const res = await fetch(
    `https://api.github.com/repos/${owner}/${repo}/commits/${sha}/pulls`,
    {
      headers: {
        Accept: 'application/vnd.github.groot-preview+json'
      }
    }
  );
  if (!res.ok) return null;
  
  const pulls = await res.json();
  if (pulls.length > 0) {
    return pulls[0].number;
  }
  return null;
}

function computePromotionStatus(env: any): 'pending' | 'promoted' | 'success' | 'failure' | 'unknown' {

  const {proposedSha, drySha: sha, checks = [], totalProposedChecks = 0, activeChecks = [],} = env;


  let promotionStatus: 'pending' | 'promoted' | 'success' | 'failure' | 'unknown' = 'unknown';

  //STATE 1: Pending (PR OPEN)
  if (proposedSha !== sha) {
    promotionStatus = 'pending';

  //STATE 1B: Pending (PR OPEN && PROPOSED CHECKS IN PROGRESS  - applies to second environment)
  } else if (proposedSha !== sha && totalProposedChecks > 0) {
    promotionStatus = 'pending';


  //STATE 2: Promoted (PR MERGED && ACTIVE CHECKS IN PROGRESS)
  } else if (proposedSha === sha) {
    if (activeChecks && activeChecks.length > 0 && !activeChecks.every((c: any) => c.status === 'success')) {
      promotionStatus = 'promoted';

      //STATE 3: Success (PR MERGED && ACTIVE CHECKS PASSED)
    } else if (activeChecks && activeChecks.length > 0 && activeChecks.every((c: any) => c.status === 'success')) {
      promotionStatus = 'success';
    }


    //STATUS 5: Failure (Failure in Proposed Checks)
  } else if (checks.some((c: any) => c.status === 'failure')) {
    promotionStatus = 'failure';

  } else {
    promotionStatus = 'unknown';
  }
  return promotionStatus;
}


export async function getEnvironmentDetails(ctp: any): Promise<any> {
  const { spec = {}, status = {} } = ctp;
  const { activeBranch, proposedBranch } = spec;
  const { active = {}, proposed = {} } = status;

  // Branches
  const branch = (activeBranch || spec.branch || '').replace(/^environments\//, '');

  // Phase
  const commitStatuses = active.commitStatuses || [];
  const phase = commitStatuses[0]?.phase || 'unknown';

  // Last Sync
  const hydrated = active.hydrated || {};
  const lastSync = hydrated.commitTime ? new Date(hydrated.commitTime).toLocaleString() : '-';

  // Hardcoded owner/repo
  const owner = 'Shirly8';
  const repo = 'argocon-gitops-promoter-hydrate-demo';

  // Dry commit
  const dry = active.dry || {};
  const drySha = dry.sha ? dry.sha.slice(0, 7) : '-';
  const { author: dryCommitAuthor, message: dryCommitMessage, url: dryCommitUrl } = await getCommitEnrichment(owner, repo, dry.sha);

  // Hydrated commit 
  const hydratedSha = hydrated.sha ? hydrated.sha.slice(0, 7) : '-';
  const { author: hydratedCommitAuthor, message: hydratedCommitMessage, url: hydratedCommitUrl } = await getCommitEnrichment(owner, repo, hydrated.sha);


  // Proposed
  const proposedDry = proposed.dry || {};
  const proposedSha = proposedDry.sha ? proposedDry.sha.slice(0, 7) : '-';
  const proposedCommitStatuses = proposed.commitStatuses || [];
  const proposedChecks = proposedCommitStatuses.map((cs: any) => ({
    name: cs.key,
    status: cs.phase || 'unknown',
    details: cs.details,
  }));

  const proposedChecksInProgress = proposedChecks.filter((c: any) => c.status === 'pending').length;
  const totalProposedChecks = proposedChecks.length;

  const passed = proposedChecks.filter((check: any) => check.status === 'success').length;
  const percent = totalProposedChecks > 0 ? (passed / totalProposedChecks) * 100 : 0;


  // Active checks
  const activeChecks = commitStatuses.map((cs: any) => ({
    name: cs.key,
    status: cs.phase || 'unknown',
    details: cs.details,
  }));
  const activeChecksInProgress = activeChecks.filter((c: any) => c.status === 'pending').length;
  const activeChecksCount = activeChecks.length;


    // PR number
    let prNumber: number | null = null;
    if (hydrated.sha) {
      prNumber = await getPRNumberFromCommit(owner, repo, hydrated.sha);
    }


  const prUrl = prNumber ? `https://github.com/${owner}/${repo}/pull/${prNumber}` : null;
  const proposedHydrated = proposed.hydrated || {};
  const prCreatedAt = proposedHydrated.commitTime || null;
  const mergeDate = hydrated.commitTime ? new Date(hydrated.commitTime).toLocaleString() : '-';


  const env = {
    branch,
    proposedBranch: proposedBranch.replace(/^environments\//, '') || null,
    phase,
    lastSync,


    drySha,
    dryCommitAuthor,
    dryCommitMessage,
    dryCommitUrl,


    hydratedSha,
    hydratedCommitAuthor,
    hydratedCommitMessage,
    hydratedCommitUrl,


    proposedSha,
    proposedChecks,
    proposedChecksInProgress,
    totalProposedChecks,
    percent,



    activeChecks,
    activeChecksInProgress,
    activeChecksCount,

    prNumber,
    prUrl,
    prCreatedAt,
    mergeDate,
  };
  return {
    ...env,

    promotionStatus: computePromotionStatus({
      ...env,
      checks: env.proposedChecks,
      totalProposedChecks: env.totalProposedChecks,
      activeChecks: env.activeChecks,
      proposedSha: env.proposedSha,
      drySha: env.drySha,
    }),
  };
}

